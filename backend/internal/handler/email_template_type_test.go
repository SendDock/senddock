package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/senddock/senddock/internal/db"
	"github.com/senddock/senddock/internal/service"
	"github.com/senddock/senddock/pkg/auth"
	"github.com/google/uuid"
)

type handlerStubFunc func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)

var (
	handlerStubOnce sync.Once
	handlerStubMu   sync.RWMutex
	handlerStubs    = map[string]handlerStubFunc{}
)

type handlerStubDriver struct{}

func (d *handlerStubDriver) Open(name string) (driver.Conn, error) {
	return &handlerStubConn{name: name}, nil
}

type handlerStubConn struct {
	name string
}

func (c *handlerStubConn) Prepare(query string) (driver.Stmt, error) {
	return &handlerStubStmt{}, nil
}

func (c *handlerStubConn) Close() error              { return nil }
func (c *handlerStubConn) Begin() (driver.Tx, error) { return &handlerStubTx{}, nil }

func (c *handlerStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	handlerStubMu.RLock()
	fn := handlerStubs[c.name]
	handlerStubMu.RUnlock()
	if fn == nil {
		return nil, errors.New("no stub handler registered")
	}
	cols, rows, err := fn(query, args)
	if err != nil {
		return nil, err
	}
	return &handlerStubRows{cols: cols, rows: rows}, nil
}

func (c *handlerStubConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type handlerStubStmt struct{}

func (s *handlerStubStmt) Close() error  { return nil }
func (s *handlerStubStmt) NumInput() int { return -1 }
func (s *handlerStubStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (s *handlerStubStmt) Query([]driver.Value) (driver.Rows, error) { return &handlerStubRows{}, nil }

type handlerStubTx struct{}

func (t *handlerStubTx) Commit() error   { return nil }
func (t *handlerStubTx) Rollback() error { return nil }

type handlerStubRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *handlerStubRows) Columns() []string { return r.cols }
func (r *handlerStubRows) Close() error      { return nil }

func (r *handlerStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

type fixedPublicURL string

func (f fixedPublicURL) PublicURL() string { return string(f) }

func openHandlerStubQueries(t *testing.T, fn handlerStubFunc) *db.Queries {
	t.Helper()
	handlerStubOnce.Do(func() {
		sql.Register("senddock_handler_stub", &handlerStubDriver{})
	})
	dsn := uuid.NewString()
	handlerStubMu.Lock()
	handlerStubs[dsn] = fn
	handlerStubMu.Unlock()
	t.Cleanup(func() {
		handlerStubMu.Lock()
		delete(handlerStubs, dsn)
		handlerStubMu.Unlock()
	})
	conn, err := sql.Open("senddock_handler_stub", dsn)
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return db.New(conn)
}

func TestSendAndCampaignHandlersReturn400ForPageTemplates(t *testing.T) {
	projectID := uuid.New()
	templateID := uuid.New()
	subscriberID := uuid.New()
	campaignID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	queries := openHandlerStubQueries(t, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "SELECT m.role FROM projects p"):
			return []string{"role"}, [][]driver.Value{{"owner"}}, nil
		case strings.Contains(query, "FROM projects WHERE id = $1"):
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
				projectID[:], userID[:], "Project", "Sender", "sender@example.test",
				"127.0.0.1", int64(2525), "user", "secret",
				nil, nil, true,
				now, now, nil, uuid.NewString(),
				nil, nil, nil,
				nil, "INBOX", false,
				uuid.NewString(), nil, false,
			}
			return cols, [][]driver.Value{row}, nil
		case strings.Contains(query, "FROM templates WHERE id = $1 AND project_id = $2"):
			cols := []string{
				"id", "project_id", "name", "subject", "html_body", "text_body",
				"variables", "created_at", "updated_at", "type",
			}
			row := []driver.Value{
				templateID[:], projectID[:], "Unsub Page", "Subject", "<p>Page</p>", "Page",
				[]byte("[]"), now, now, "page",
			}
			return cols, [][]driver.Value{row}, nil
		default:
			return nil, nil, errors.New("unexpected query: " + query)
		}
	})

	pubURL := fixedPublicURL("https://mail.example.com")
	projects := service.NewProjectService(queries, "test-secret")
	emails := service.NewEmailService(queries, pubURL, "test-secret", nil, nil, nil)
	campaigns := service.NewCampaignService(queries)

	emailH := NewEmailHandler(emails, projects, nil)
	campaignH := NewCampaignHandler(campaigns, projects, nil, pubURL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/projects/{id}/send", emailH.Send)
	mux.HandleFunc("POST /api/v1/projects/{id}/send/batch", emailH.BatchSend)
	mux.HandleFunc("POST /api/v1/projects/{id}/broadcast", emailH.Broadcast)
	mux.HandleFunc("POST /api/v1/projects/{id}/campaigns", campaignH.Create)
	mux.HandleFunc("PATCH /api/v1/projects/{id}/campaigns/{campaignId}", campaignH.Update)

	scheduledAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	base := "/api/v1/projects/" + projectID.String()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "send to address",
			method: http.MethodPost,
			path:   base + "/send",
			body:   `{"to":"ada@example.test","template_id":"` + templateID.String() + `"}`,
		},
		{
			name:   "send to subscriber",
			method: http.MethodPost,
			path:   base + "/send",
			body:   `{"subscriber_id":"` + subscriberID.String() + `","template_id":"` + templateID.String() + `"}`,
		},
		{
			name:   "batch send",
			method: http.MethodPost,
			path:   base + "/send/batch",
			body:   `{"template_id":"` + templateID.String() + `","recipients":[{"to":"ada@example.test"}]}`,
		},
		{
			name:   "broadcast",
			method: http.MethodPost,
			path:   base + "/broadcast",
			body:   `{"template_id":"` + templateID.String() + `"}`,
		},
		{
			name:   "campaign create",
			method: http.MethodPost,
			path:   base + "/campaigns",
			body:   `{"name":"Campaign","template_id":"` + templateID.String() + `","scheduled_at":"` + scheduledAt + `"}`,
		},
		{
			name:   "campaign update",
			method: http.MethodPatch,
			path:   base + "/campaigns/" + campaignID.String(),
			body:   `{"name":"Campaign","template_id":"` + templateID.String() + `","scheduled_at":"` + scheduledAt + `"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), auth.UserIDKey, userID.String()))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), service.ErrNotEmailTemplate.Error()) {
				t.Fatalf("response body = %s, want %q", rec.Body.String(), service.ErrNotEmailTemplate.Error())
			}
		})
	}
}
