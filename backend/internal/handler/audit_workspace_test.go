package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/senddock/senddock/internal/db"
	"github.com/senddock/senddock/internal/service"
	"github.com/senddock/senddock/pkg/auth"
)

// Workspace actions — creating a workspace, renaming it, adding or removing a member — have
// no project to be filed under. They used to be written with an empty project and were
// therefore invisible in an audit log filtered by project, so the test drives the real
// handler and reads the row it wrote.
//
// It skips unless DATABASE_URL points at a migrated instance.

func auditTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; skipping workspace audit integration test")
	}

	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return conn, db.New(conn)
}

// failInsertsIntoAudit makes every audit write fail, which is how the contract below is
// checked without breaking the database for anything else.
func failInsertsIntoAudit(t *testing.T, conn *sql.DB) {
	t.Helper()

	const trigger = "senddock_test_fail_audit_insert"
	drop := func() {
		_, _ = conn.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS `+trigger+` ON audit_log`)
		_, _ = conn.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS `+trigger+`()`)
	}
	drop()

	if _, err := conn.ExecContext(context.Background(),
		`CREATE FUNCTION `+trigger+`() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit write forced to fail'; END; $$ LANGUAGE plpgsql`,
	); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(),
		`CREATE TRIGGER `+trigger+` BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION `+trigger+`()`,
	); err != nil {
		drop()
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(drop)
}

func TestAWorkspaceActionIsAuditedAgainstItsWorkspace(t *testing.T) {
	conn, queries := auditTestDB(t)

	ctx := context.Background()

	var userID, workspaceID uuid.UUID
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id`,
		"audit-workspace-"+uuid.NewString()+"@example.test", "Audit Workspace Test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO workspaces (name, created_by) VALUES ($1, $2) RETURNING id`,
		"audit-workspace-test", userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		// The audit rows cascade with the workspace and the user.
		if _, err := conn.ExecContext(ctx, `DELETE FROM audit_log WHERE workspace_id = $1`, workspaceID); err != nil {
			t.Logf("cleanup audit rows: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID); err != nil {
			t.Logf("cleanup workspace: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID,
	); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	h := NewWorkspaceHandler(service.NewWorkspaceService(queries, conn), service.NewProjectService(queries, "test-encryption-secret"))
	h.Audit = service.NewAuditService(queries)

	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}", h.Rename)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+workspaceID.String(),
		strings.NewReader(`{"name":"renamed by the test"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.UserIDKey, userID.String()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var (
		auditedWorkspace uuid.NullUUID
		auditedProject   uuid.NullUUID
	)
	if err := conn.QueryRowContext(ctx,
		`SELECT workspace_id, project_id FROM audit_log
		  WHERE action = 'workspace.rename' AND workspace_id = $1`, workspaceID,
	).Scan(&auditedWorkspace, &auditedProject); err != nil {
		t.Fatalf("the workspace action was not audited against its workspace: %v", err)
	}

	if !auditedWorkspace.Valid || auditedWorkspace.UUID != workspaceID {
		t.Errorf("audited workspace = %v, want %s", auditedWorkspace, workspaceID)
	}
	if auditedProject.Valid {
		t.Errorf("audited project = %v, want none: a workspace action belongs to no project", auditedProject)
	}
}

// A project action has to keep landing in its project, so the reading side of the audit log
// (which filters by project) does not lose what it used to show.
func TestAProjectActionIsAuditedAgainstItsProject(t *testing.T) {
	conn, queries := auditTestDB(t)
	ctx := context.Background()

	var userID, workspaceID, projectID uuid.UUID
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id`,
		"audit-project-"+uuid.NewString()+"@example.test", "Audit Project Test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO workspaces (name, created_by) VALUES ($1, $2) RETURNING id`,
		"audit-project-test", userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO projects (workspace_id, user_id, name, from_email) VALUES ($1, $2, $3, $4) RETURNING id`,
		workspaceID, userID, "audit-project-test", "sender@example.test",
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(ctx, `DELETE FROM audit_log WHERE project_id = $1`, projectID); err != nil {
			t.Logf("cleanup audit rows: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID); err != nil {
			t.Logf("cleanup workspace: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	audit := service.NewAuditService(queries)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/something", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	audit.LogFromRequest(req, projectID.String(), userID.String(), "project.test", "project", projectID.String(), nil)

	var (
		auditedProject   uuid.NullUUID
		auditedWorkspace uuid.NullUUID
	)
	if err := conn.QueryRowContext(ctx,
		`SELECT project_id, workspace_id FROM audit_log WHERE action = 'project.test' AND project_id = $1`, projectID,
	).Scan(&auditedProject, &auditedWorkspace); err != nil {
		t.Fatalf("the project action was not audited against its project: %v", err)
	}
	if !auditedProject.Valid || auditedProject.UUID != projectID {
		t.Errorf("audited project = %v, want %s", auditedProject, projectID)
	}
	if auditedWorkspace.Valid {
		t.Errorf("audited workspace = %v, want none", auditedWorkspace)
	}
}

// Auditing is a record of what happened, not a step the operation depends on. A failing
// audit write must not take the action down with it — the alternative is that a full disk,
// a bad migration or a lock on one table stops people from renaming a workspace.
func TestARenamedWorkspaceSurvivesAFailedAuditWrite(t *testing.T) {
	conn, queries := auditTestDB(t)
	ctx := context.Background()

	var userID, workspaceID uuid.UUID
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id`,
		"audit-fail-"+uuid.NewString()+"@example.test", "Audit Fail Test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO workspaces (name, created_by) VALUES ($1, $2) RETURNING id`,
		"audit-fail-test", userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = conn.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID,
	); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	h := NewWorkspaceHandler(service.NewWorkspaceService(queries, conn), service.NewProjectService(queries, "test-encryption-secret"))
	h.Audit = service.NewAuditService(queries)

	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}", h.Rename)

	failInsertsIntoAudit(t, conn)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+workspaceID.String(),
		strings.NewReader(`{"name":"renamed with no audit row"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.UserIDKey, userID.String()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d with the audit write failing, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var name string
	if err := conn.QueryRowContext(ctx, `SELECT name FROM workspaces WHERE id = $1`, workspaceID).Scan(&name); err != nil {
		t.Fatalf("read back workspace: %v", err)
	}
	if name != "renamed with no audit row" {
		t.Errorf("workspace name = %q, want the rename to have gone through", name)
	}

	// Without this the test would pass on a run where nothing failed at all: the audit row
	// has to be missing for the assertion above to mean anything.
	var auditRows int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'workspace.rename' AND workspace_id = $1`, workspaceID,
	).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditRows != 0 {
		t.Fatalf("the audit write was expected to fail and did not (%d rows), so this test proved nothing", auditRows)
	}
}
