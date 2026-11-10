package service

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/senddock/senddock/internal/db"
	"github.com/google/uuid"
)

func TestClassifyBounceKeepsErrorChain(t *testing.T) {
	orig := &textproto.Error{Code: 550, Msg: "user unknown"}
	be := classifyBounce(orig)
	if be == nil {
		t.Fatal("expected a BounceError for a 550")
	}
	if be.Code != 550 || be.Message != "user unknown" || be.Stage != "rcpt" {
		t.Fatalf("unexpected fields: %+v", be)
	}

	var protoErr *textproto.Error
	if !errors.As(be, &protoErr) {
		t.Fatal("BounceError must unwrap to the original *textproto.Error")
	}
	if protoErr.Code != 550 {
		t.Fatalf("unwrapped code = %d, want 550", protoErr.Code)
	}
}

func TestClassifyBounceIgnoresNonBounce(t *testing.T) {
	cases := map[string]error{
		"nil":     nil,
		"4xx":     &textproto.Error{Code: 421, Msg: "try later"},
		"wrapped": fmt.Errorf("smtp auth failed: %w", errors.New("bad credentials")),
	}
	for name, err := range cases {
		if be := classifyBounce(err); be != nil {
			t.Errorf("%s: expected nil, got %+v", name, be)
		}
	}
}

func newPipeSMTPConn(t *testing.T, failStage string, failLine string) net.Conn {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		writeLine := func(line string) {
			_, _ = fmt.Fprintf(serverConn, "%s\r\n", line)
		}

		writeLine("220 localhost ESMTP")
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if inData {
				if trimmed == "." {
					inData = false
					if failStage == "data_close" {
						writeLine(failLine)
					} else {
						writeLine("250 2.0.0 queued")
					}
				}
				continue
			}

			upper := strings.ToUpper(trimmed)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				writeLine("250-localhost")
				writeLine("250 AUTH PLAIN")
			case strings.HasPrefix(upper, "AUTH"):
				writeLine("235 2.7.0 Authentication successful")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				if failStage == "mail" {
					writeLine(failLine)
				} else {
					writeLine("250 2.1.0 Ok")
				}
			case strings.HasPrefix(upper, "RCPT TO:"):
				if failStage == "rcpt" {
					writeLine(failLine)
				} else {
					writeLine("250 2.1.5 Ok")
				}
			case strings.HasPrefix(upper, "DATA"):
				if failStage == "data_cmd" {
					writeLine(failLine)
				} else {
					inData = true
					writeLine("354 End data with <CR><LF>.<CR><LF>")
				}
			case strings.HasPrefix(upper, "QUIT"):
				writeLine("221 2.0.0 Bye")
				return
			default:
				writeLine("250 Ok")
			}
		}
	}()

	return clientConn
}

func TestDeliverSMTPClassifies5xxAcrossMailRcptAndData(t *testing.T) {
	cases := []struct {
		name      string
		failStage string
		failLine  string
		wantCode  int
		wantStage string
	}{
		{
			name:      "550 during MAIL FROM",
			failStage: "mail",
			failLine:  "550 5.1.7 Sender address rejected",
			wantCode:  550,
			wantStage: "mail",
		},
		{
			name:      "550 during RCPT TO",
			failStage: "rcpt",
			failLine:  "550 5.1.1 User unknown",
			wantCode:  550,
			wantStage: "rcpt",
		},
		{
			name:      "554 during DATA command",
			failStage: "data_cmd",
			failLine:  "554 5.7.1 Transaction failed",
			wantCode:  554,
			wantStage: "data",
		},
		{
			name:      "554 at end of DATA",
			failStage: "data_close",
			failLine:  "554 5.7.1 Message rejected as spam",
			wantCode:  554,
			wantStage: "data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newPipeSMTPConn(t, tc.failStage, tc.failLine)
			err := deliverSMTPConn(conn, "localhost", "user", "pass", "sender@example.com", "recipient@example.com", []byte("Subject: Test\r\n\r\nBody\r\n"), false, false)
			if err == nil {
				t.Fatal("expected bounce error, got nil")
			}
			var be *BounceError
			if !errors.As(err, &be) {
				t.Fatalf("expected *BounceError, got %T (%v)", err, err)
			}
			if be.Code != tc.wantCode {
				t.Fatalf("BounceError.Code = %d, want %d", be.Code, tc.wantCode)
			}
			if be.Stage != tc.wantStage {
				t.Fatalf("BounceError.Stage = %q, want %q", be.Stage, tc.wantStage)
			}
		})
	}
}

func TestTrackAndSend5xxDuringDataMarksBouncedAndSuppresses(t *testing.T) {
	projectID := uuid.New()
	logID := uuid.New()

	var loggedStatus string
	var suppressionSource string

	_, queries := openStubQueries(t, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "INSERT INTO email_logs"):
			return []string{
				"id", "project_id", "subscriber_id", "template_id", "to_email", "subject",
				"status", "error", "sent_at", "OpenedAt", "ClickedAt",
				"broadcast_id", "complained_at", "newsletter_id",
			}, [][]driver.Value{{
				logID[:], projectID[:], nil, nil, "recipient@example.com", "Hello",
				"pending", nil, time.Now().UTC(), nil, nil,
				nil, nil, nil,
			}}, nil
		case strings.Contains(query, "UPDATE email_logs"):
			loggedStatus, _ = args[1].Value.(string)
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "INSERT INTO suppressions"):
			suppressionSource, _ = args[3].Value.(string)
			return []string{"id", "project_id", "email", "reason", "source", "created_at"}, [][]driver.Value{{
				uuid.NewString(), projectID[:], "recipient@example.com", "bounce", suppressionSource, time.Now().UTC(),
			}}, nil
		default:
			return nil, nil, nil
		}
	})

	svc := NewEmailService(queries, staticPublicURL("http://localhost:8080"), "secret", nil, nil, NewSuppressionService(queries))
	svc.deliverFn = func(host, _ string, user, pass, from, to string, msg []byte, implicitTLS bool, _, _ time.Duration, allowInsecureTLS bool) error {
		conn := newPipeSMTPConn(t, "data_close", "554 5.7.1 Message rejected")
		return deliverSMTPConn(conn, host, user, pass, from, to, msg, implicitTLS, allowInsecureTLS)
	}

	project := db.Project{
		ID:                    projectID,
		SmtpHost:              sql.NullString{String: "localhost", Valid: true},
		SmtpPort:              sql.NullInt32{Int32: 2525, Valid: true},
		SmtpUser:              sql.NullString{String: "user", Valid: true},
		SmtpPasswordEncrypted: sql.NullString{String: "pass", Valid: true},
		FromEmail:             sql.NullString{String: "sender@example.com", Valid: true},
	}

	err := svc.trackAndSend(context.Background(), project, projectID, uuid.NullUUID{}, uuid.NullUUID{}, "recipient@example.com", "Hello", bodyParts{html: "<p>Hi</p>"}, "", uuid.NullUUID{}, uuid.NullUUID{})
	var bounce *BounceError
	if !errors.As(err, &bounce) {
		t.Fatalf("expected *BounceError, got %v", err)
	}
	if loggedStatus != "bounced" {
		t.Fatalf("expected email log status 'bounced', got %q", loggedStatus)
	}
	if !strings.HasPrefix(suppressionSource, "smtp 554 during data:") {
		t.Fatalf("expected suppression source with 'smtp 554 during data:', got %q", suppressionSource)
	}
}

func TestBroadcastWorkerProcessJob554DuringDataMarksBouncedWithoutRetry(t *testing.T) {
	projectID := uuid.New()
	templateID := uuid.New()
	subscriberID := uuid.New()
	broadcastID := uuid.New()
	jobID := uuid.New()
	logID := uuid.New()

	markedJobBounced := false
	scheduledRetry := false

	_, queries := openStubQueries(t, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM broadcasts WHERE id = $1"):
			return []string{
				"id", "project_id", "template_id", "subject", "variables",
				"status", "total_recipients", "sent_count", "failed_count", "suppressed_count",
				"started_at", "finished_at", "html_fields", "newsletter_id",
			}, [][]driver.Value{{
				broadcastID[:], projectID[:], templateID[:], "Hello", []byte(`{}`),
				"running", int64(1), int64(0), int64(0), int64(0),
				time.Now().UTC(), nil, []byte(`[]`), nil,
			}}, nil
		case strings.Contains(query, "FROM projects WHERE id = $1"):
			cols, rows := stubProjectRow(projectID)
			return cols, rows, nil
		case strings.Contains(query, "FROM templates WHERE id = $1"):
			cols, rows := stubTemplateRow(templateID, projectID, "email")
			return cols, rows, nil
		case strings.Contains(query, "FROM subscribers WHERE id = $1"):
			return stubSubscriberColumns(), [][]driver.Value{
				stubSubscriberValueRow(subscriberID, projectID, "recipient@example.com", "Recipient", "subscribed"),
			}, nil
		case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "FROM suppressions"):
			return []string{"exists"}, [][]driver.Value{{false}}, nil
		case strings.Contains(query, "INSERT INTO email_logs"):
			return []string{
				"id", "project_id", "subscriber_id", "template_id", "to_email", "subject",
				"status", "error", "sent_at", "opened_at", "clicked_at",
				"broadcast_id", "complained_at", "newsletter_id",
			}, [][]driver.Value{{
				logID[:], projectID[:], subscriberID[:], templateID[:], "recipient@example.com", "Hello",
				"pending", nil, time.Now().UTC(), nil, nil,
				broadcastID[:], nil, nil,
			}}, nil
		case strings.Contains(query, "INSERT INTO suppressions"):
			return []string{"id", "project_id", "email_normalized", "reason", "source", "created_at", "last_seen_at"}, [][]driver.Value{{
				uuid.NewString(), projectID[:], "recipient@example.com", "bounce", "smtp 554 during data", time.Now().UTC(), time.Now().UTC(),
			}}, nil
		case strings.Contains(query, "UPDATE broadcast_jobs") && strings.Contains(query, "status = 'bounced'"):
			markedJobBounced = true
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "UPDATE broadcast_jobs") && strings.Contains(query, "status = 'retry'"):
			scheduledRetry = true
			return nil, [][]driver.Value{{1}}, nil
		default:
			return nil, [][]driver.Value{{1}}, nil
		}
	})

	emailSvc := NewEmailService(queries, staticPublicURL("https://mail.example.com"), "secret", nil, nil, NewSuppressionService(queries))
	emailSvc.deliverFn = func(host, _ string, user, pass, from, to string, msg []byte, implicitTLS bool, _, _ time.Duration, allowInsecureTLS bool) error {
		conn := newPipeSMTPConn(t, "data_close", "554 5.7.1 Message rejected")
		return deliverSMTPConn(conn, host, user, pass, from, to, msg, implicitTLS, allowInsecureTLS)
	}

	worker := NewBroadcastWorker(queries, emailSvc)
	worker.processJob(context.Background(), db.BroadcastJob{
		ID:           jobID,
		BroadcastID:  broadcastID,
		ProjectID:    projectID,
		SubscriberID: subscriberID,
		Attempts:     1,
	})

	if !markedJobBounced {
		t.Fatal("expected broadcast job to be marked bounced on 554 during DATA")
	}
	if scheduledRetry {
		t.Fatal("expected broadcast job not to be scheduled for retry on 554 during DATA")
	}
}
