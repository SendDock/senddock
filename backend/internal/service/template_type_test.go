package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/senddock/senddock/internal/db"
	"github.com/google/uuid"
)

type queryStubHandler func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)

var (
	stubDriverOnce sync.Once
	stubHandlersMu sync.RWMutex
	stubHandlers   = map[string]queryStubHandler{}
)

type stubDriver struct{}

func (d *stubDriver) Open(name string) (driver.Conn, error) {
	return &stubConn{name: name}, nil
}

type stubConn struct {
	name string
}

func (c *stubConn) Prepare(query string) (driver.Stmt, error) {
	return &stubStmt{name: c.name, query: query}, nil
}

func (c *stubConn) Close() error              { return nil }
func (c *stubConn) Begin() (driver.Tx, error) { return &stubTx{}, nil }

func (c *stubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stubHandlersMu.RLock()
	fn := stubHandlers[c.name]
	stubHandlersMu.RUnlock()
	if fn == nil {
		return nil, errors.New("no stub handler registered")
	}
	cols, rows, err := fn(query, args)
	if err != nil {
		return nil, err
	}
	return &stubRows{cols: cols, rows: rows}, nil
}

func (c *stubConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	stubHandlersMu.RLock()
	fn := stubHandlers[c.name]
	stubHandlersMu.RUnlock()
	if fn == nil {
		return driver.RowsAffected(0), nil
	}
	_, rows, err := fn(query, args)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(len(rows)), nil
}

type stubStmt struct {
	name  string
	query string
}

func (s *stubStmt) Close() error  { return nil }
func (s *stubStmt) NumInput() int { return -1 }

func (s *stubStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (s *stubStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &stubRows{}, nil
}

type stubTx struct{}

func (t *stubTx) Commit() error   { return nil }
func (t *stubTx) Rollback() error { return nil }

type stubRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *stubRows) Columns() []string { return r.cols }
func (r *stubRows) Close() error      { return nil }

func (r *stubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func openStubQueries(t *testing.T, fn queryStubHandler) (*sql.DB, *db.Queries) {
	t.Helper()
	stubDriverOnce.Do(func() {
		sql.Register("senddock_service_stub", &stubDriver{})
	})
	dsn := uuid.NewString()
	stubHandlersMu.Lock()
	stubHandlers[dsn] = fn
	stubHandlersMu.Unlock()
	t.Cleanup(func() {
		stubHandlersMu.Lock()
		delete(stubHandlers, dsn)
		stubHandlersMu.Unlock()
	})
	conn, err := sql.Open("senddock_service_stub", dsn)
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, db.New(conn)
}

func stubProjectRow(projectID uuid.UUID) ([]string, [][]driver.Value) {
	now := time.Now().UTC()
	cols := []string{
		"id", "user_id", "name", "from_name", "from_email",
		"smtp_host", "smtp_port", "smtp_user", "smtp_password_encrypted",
		"webhook_url", "webhook_secret", "tracking_enabled",
		"created_at", "updated_at", "description", "bounce_token",
		"bounce_imap_host", "bounce_imap_port", "bounce_imap_user",
		"bounce_imap_password_encrypted", "bounce_imap_folder", "bounce_imap_enabled",
		"workspace_id", "unsubscribe_template_id", "smtp_allow_insecure_tls",
	}
	row := []driver.Value{
		projectID[:], uuid.NewString(), "Test Project", "Sender", "sender@example.test",
		"127.0.0.1", int64(2525), "user", "secret",
		nil, nil, true,
		now, now, nil, uuid.NewString(),
		nil, nil, nil,
		nil, "INBOX", false,
		uuid.NewString(), nil, false,
	}
	return cols, [][]driver.Value{row}
}

func stubTemplateRow(templateID, projectID uuid.UUID, templateType string) ([]string, [][]driver.Value) {
	now := time.Now().UTC()
	cols := []string{
		"id", "project_id", "name", "subject", "html_body", "text_body",
		"variables", "created_at", "updated_at", "type",
	}
	row := []driver.Value{
		templateID[:], projectID[:], "Unsubscribe Page", "Subject", "<p>Body</p>", "Body",
		[]byte("[]"), now, now, templateType,
	}
	return cols, [][]driver.Value{row}
}

func TestEmailServiceRejectsPageTemplates(t *testing.T) {
	projectID := uuid.New()
	templateID := uuid.New()
	subscriberID := uuid.New()

	_, queries := openStubQueries(t, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM projects WHERE id = $1"):
			cols, rows := stubProjectRow(projectID)
			return cols, rows, nil
		case strings.Contains(query, "FROM templates WHERE id = $1 AND project_id = $2"):
			cols, rows := stubTemplateRow(templateID, projectID, "page")
			return cols, rows, nil
		default:
			return nil, nil, errors.New("unexpected query: " + query)
		}
	})

	svc := NewEmailService(queries, staticPublicURL("https://mail.example.com"), "test-secret", nil, nil, nil)
	ctx := WithSystemContext(context.Background())

	if _, err := svc.SendToSubscriber(ctx, projectID.String(), subscriberID.String(), templateID.String()); !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("SendToSubscriber err = %v, want %v", err, ErrNotEmailTemplate)
	}

	if _, err := svc.Broadcast(ctx, projectID.String(), templateID.String(), "", nil, nil, "", ""); !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("Broadcast err = %v, want %v", err, ErrNotEmailTemplate)
	}

	if err := svc.SendWithTemplate(ctx, projectID.String(), templateID.String(), "ada@example.test", "", nil, nil); !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("SendWithTemplate err = %v, want %v", err, ErrNotEmailTemplate)
	}

	outcome, err := svc.SendBroadcastJob(
		ctx,
		db.BroadcastJob{ID: uuid.New(), ProjectID: projectID, SubscriberID: subscriberID},
		db.Broadcast{ID: uuid.New(), ProjectID: projectID, TemplateID: templateID},
	)
	if !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("SendBroadcastJob err = %v, want %v", err, ErrNotEmailTemplate)
	}
	if outcome != BroadcastJobOutcomeBounced {
		t.Fatalf("SendBroadcastJob outcome = %v, want %v", outcome, BroadcastJobOutcomeBounced)
	}
}

func TestCampaignServiceRejectsPageTemplates(t *testing.T) {
	projectID := uuid.New()
	templateID := uuid.New()
	campaignID := uuid.New()

	_, queries := openStubQueries(t, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		if strings.Contains(query, "FROM templates WHERE id = $1 AND project_id = $2") {
			cols, rows := stubTemplateRow(templateID, projectID, "page")
			return cols, rows, nil
		}
		return nil, nil, errors.New("unexpected query: " + query)
	})

	campaigns := NewCampaignService(queries)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)

	if _, err := campaigns.Create(ctx, projectID.String(), templateID.String(), "Launch", "Subject", future, []byte("{}"), ""); !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("CampaignService.Create err = %v, want %v", err, ErrNotEmailTemplate)
	}

	if _, err := campaigns.Update(ctx, campaignID.String(), projectID.String(), templateID.String(), "Launch", "Subject", future, []byte("{}"), ""); !errors.Is(err, ErrNotEmailTemplate) {
		t.Fatalf("CampaignService.Update err = %v, want %v", err, ErrNotEmailTemplate)
	}
}
