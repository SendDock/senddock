package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/senddock/senddock/internal/db"
)

// This drives a real message through the whole send path — template rendering, the
// plain-text part, tracking rewrites, the SMTP conversation — into a capture server
// listening on the loopback interface, and then reads what actually arrived.
//
// It skips unless DATABASE_URL points at a migrated instance, because the send path
// records an email log row before it talks to the relay.

// captureSMTPServer accepts one SMTP session at a time and keeps every message it is
// handed, so a test can assert on the bytes that went over the wire rather than on the
// function that produced them.
type captureSMTPServer struct {
	listener net.Listener
	host     string
	port     int

	mu       sync.Mutex
	messages [][]byte
}

func newCaptureSMTPServer(t *testing.T) *captureSMTPServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	server := &captureSMTPServer{listener: listener, host: "127.0.0.1", port: listener.Addr().(*net.TCPAddr).Port}
	go server.serve()

	return server
}

func (s *captureSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *captureSMTPServer) handle(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	reply := func(line string) {
		writer.WriteString(line + "\r\n")
		writer.Flush()
	}

	reply("220 capture ESMTP ready")

	var (
		inData  bool
		message []string
	)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				s.mu.Lock()
				s.messages = append(s.messages, []byte(strings.Join(message, "\r\n")))
				s.mu.Unlock()
				message = nil
				reply("250 2.0.0 Ok: queued")
				continue
			}
			// Leading dots are doubled on the wire so a body line cannot end the data.
			message = append(message, strings.TrimPrefix(line, "."))
			continue
		}

		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			writer.WriteString("250-capture\r\n250-AUTH PLAIN\r\n250-8BITMIME\r\n250 OK\r\n")
			writer.Flush()
		case strings.HasPrefix(line, "AUTH"):
			reply("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			reply("250 2.1.0 Ok")
		case strings.HasPrefix(line, "DATA"):
			reply("354 End data with <CR><LF>.<CR><LF>")
			inData = true
		case strings.HasPrefix(line, "QUIT"):
			reply("221 2.0.0 Bye")
			return
		default:
			reply("250 2.0.0 Ok")
		}
	}
}

func (s *captureSMTPServer) received(t *testing.T) []byte {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.messages) == 0 {
		t.Fatal("the relay received no message")
	}
	return s.messages[0]
}

type sendFixture struct {
	queries      *db.Queries
	conn         *sql.DB
	service      *EmailService
	projectID    string
	subscriberID string
	templateID   string
	server       *captureSMTPServer
}

func newSendFixture(t *testing.T, htmlBody, textBody string) sendFixture {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; skipping end-to-end send test")
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

	server := newCaptureSMTPServer(t)
	ctx := context.Background()

	const encSecret = "test-encryption-secret-that-is-long-enough"
	encryptedPassword, err := Encrypt("smtp-test-password", encSecret)
	if err != nil {
		t.Fatalf("encrypt smtp password: %v", err)
	}

	var userID, workspaceID, projectID, subscriberID, templateID uuid.UUID
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id`,
		"send-e2e-"+uuid.NewString()+"@example.test", "Send E2E",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO workspaces (name, created_by) VALUES ($1, $2) RETURNING id`,
		"send-e2e", userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID); err != nil {
			t.Logf("cleanup workspace: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID,
	); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO projects (workspace_id, user_id, name, from_email, from_name,
		                       smtp_host, smtp_port, smtp_user, smtp_password_encrypted)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		workspaceID, userID, "send-e2e", "news@example.test", "SendDock Test",
		server.host, server.port, "relay-user", encryptedPassword,
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO subscribers (project_id, email, name, status) VALUES ($1, $2, $3, 'active') RETURNING id`,
		projectID, "ada@example.test", "Ada",
	).Scan(&subscriberID); err != nil {
		t.Fatalf("insert subscriber: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`INSERT INTO templates (project_id, name, subject, html_body, text_body, type)
		 VALUES ($1, $2, $3, $4, $5, 'email') RETURNING id`,
		projectID, "send-e2e", "Hola {{name}}", htmlBody, textBody,
	).Scan(&templateID); err != nil {
		t.Fatalf("insert template: %v", err)
	}

	queries := db.New(conn)
	return sendFixture{
		queries:      queries,
		conn:         conn,
		service:      NewEmailService(queries, staticPublicURL("https://senddock.test"), encSecret, nil, nil, nil),
		projectID:    projectID.String(),
		subscriberID: subscriberID.String(),
		templateID:   templateID.String(),
		server:       server,
	}
}

// parseMessage splits what the relay received into its MIME parts, keyed by content type.
func parseMessage(t *testing.T, raw []byte) map[string]string {
	t.Helper()

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("received message is not valid mail: %v\n%s", err, raw)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type is not valid: %v", err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, want multipart/alternative\n%s", mediaType, raw)
	}

	parts := map[string]string{}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part body: %v", err)
		}
		contentType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("part Content-Type is not valid: %v", err)
		}
		parts[contentType] = string(body)
	}

	return parts
}

// A template's authored text_body has to reach the wire, with the same variable
// substitution the HTML gets.
func TestSendToSubscriberPutsBothPartsOnTheWire(t *testing.T) {
	fixture := newSendFixture(t,
		`<p>Hola {{name}}</p><p><a href="{{unsubscribe_url}}">Unsubscribe</a></p>`,
		"Hola {{name}}\n\nUnsubscribe: {{unsubscribe_url}}",
	)

	result, err := fixture.service.SendToSubscriber(
		WithSystemContext(context.Background()),
		fixture.projectID, fixture.subscriberID, fixture.templateID,
	)
	if err != nil {
		t.Fatalf("SendToSubscriber: %v", err)
	}
	if result.Sent != 1 {
		t.Fatalf("send result = %+v, want one sent", result)
	}

	parts := parseMessage(t, fixture.server.received(t))

	text, ok := parts["text/plain"]
	if !ok {
		t.Fatalf("message has no plain-text part, only %v", keys(parts))
	}
	htmlPart, ok := parts["text/html"]
	if !ok {
		t.Fatalf("message has no html part, only %v", keys(parts))
	}

	if !strings.Contains(text, "Hola Ada") {
		t.Errorf("plain-text part did not get the name substituted: %q", text)
	}
	if !strings.Contains(text, "https://senddock.test") {
		t.Errorf("plain-text part did not get the unsubscribe url substituted: %q", text)
	}
	for _, leftover := range []string{"{{name}}", "{{unsubscribe_url}}"} {
		if strings.Contains(text, leftover) {
			t.Errorf("plain-text part still carries %s: %q", leftover, text)
		}
	}

	if !strings.Contains(htmlPart, "Hola Ada") {
		t.Errorf("html part did not get the name substituted: %q", htmlPart)
	}

	// The tracking pixel and the rewritten links belong to the HTML part. A pixel in the
	// text part would show up as a stray URL to a reader, and a rewritten link would send
	// them through a redirect for no reason.
	if strings.Contains(text, "<img") || strings.Contains(text, ".gif") {
		t.Errorf("the tracking pixel leaked into the plain-text part: %q", text)
	}
	if !strings.Contains(htmlPart, "<img") || !strings.Contains(htmlPart, ".gif") {
		t.Errorf("the html part is missing its tracking pixel: %q", htmlPart)
	}

	t.Logf("text part:\n%s\nhtml part:\n%s", text, htmlPart)
}

// A template saved without a text_body still has to ship a text part, which is the
// promise the docs make and the reason the derivation lives in the template service.
func TestSendToSubscriberDerivesTextWhenTheTemplateHasNone(t *testing.T) {
	html := `<p>Hola {{name}}</p><p><a href="{{unsubscribe_url}}">Unsubscribe</a></p>`
	derived := resolveTextBody(html, "")
	if derived == "" {
		t.Fatal("the template service derived no text from the html")
	}

	fixture := newSendFixture(t, html, derived)

	if _, err := fixture.service.SendToSubscriber(
		WithSystemContext(context.Background()),
		fixture.projectID, fixture.subscriberID, fixture.templateID,
	); err != nil {
		t.Fatalf("SendToSubscriber: %v", err)
	}

	parts := parseMessage(t, fixture.server.received(t))
	text, ok := parts["text/plain"]
	if !ok {
		t.Fatalf("message has no plain-text part, only %v", keys(parts))
	}

	if !strings.Contains(text, "Hola Ada") {
		t.Errorf("derived text part did not get the name substituted: %q", text)
	}
	// The link the HTML carries has to survive into the text, or a reader of that part
	// has no way to unsubscribe.
	if !strings.Contains(text, "https://senddock.test") {
		t.Errorf("derived text part lost the unsubscribe link: %q", text)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The derivation lives in the template service so that every way a template can be saved —
// the API, the library install, the dashboard — gets a text part, not only the path that
// happens to go through the editor.
func TestTemplateServiceGeneratesTextBodyOnSave(t *testing.T) {
	fixture := newSendFixture(t, "", "")
	templates := NewTemplateService(fixture.queries)
	ctx := context.Background()

	created, err := templates.Create(ctx, fixture.projectID, "generated", "Subject", `<p>Hola {{name}}</p>`, "", "email")
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if created.TextBody != "Hola {{name}}" {
		t.Errorf("created text_body = %q, want the text derived from the html", created.TextBody)
	}

	authored, err := templates.Create(ctx, fixture.projectID, "authored", "Subject", `<p>Hola</p>`, "Mi propio texto", "email")
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if authored.TextBody != "Mi propio texto" {
		t.Errorf("created text_body = %q, want the authored value kept as it is", authored.TextBody)
	}

	updated, err := templates.Update(ctx, created.ID.String(), fixture.projectID, "generated", "Subject", `<p>Adios {{name}}</p>`, "")
	if err != nil {
		t.Fatalf("update template: %v", err)
	}
	if updated.TextBody != "Adios {{name}}" {
		t.Errorf("updated text_body = %q, want it derived from the new html", updated.TextBody)
	}
}
