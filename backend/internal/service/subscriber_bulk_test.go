package service

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arkhe-systems/senddock/internal/webhooks"
	"github.com/google/uuid"
)

type captureDispatcher struct {
	mu     sync.Mutex
	events []webhooks.Event
}

func (d *captureDispatcher) Enqueue(_ context.Context, ev webhooks.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, ev)
}

func (d *captureDispatcher) list() []webhooks.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]webhooks.Event, len(d.events))
	copy(out, d.events)
	return out
}

func stubSubscriberColumns() []string {
	return []string{
		"id", "project_id", "email", "name", "status", "metadata",
		"subscribed_at", "unsubscribed_at", "created_at", "updated_at", "tags",
	}
}

func stubSubscriberValueRow(id, projectID uuid.UUID, email, name, status string) []driver.Value {
	now := time.Now().UTC()
	return []driver.Value{
		id[:], projectID[:], email, name, status, []byte("{}"),
		now, now, now, now, "{}",
	}
}

func TestBulkImportDispatchesSubscriberCreatedPerImportedRow(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()

	_, queries := openStubQueries(t, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		if strings.Contains(query, "INSERT INTO subscribers") {
			email, _ := args[1].Value.(string)
			name, _ := args[2].Value.(string)
			status, _ := args[3].Value.(string)
			if email == "dup@example.test" {
				return nil, nil, errors.New("duplicate key value violates unique constraint")
			}
			row := []driver.Value{
				uuid.NewString(), projectID[:], email, name, status, []byte("{}"),
				now, nil, now, now, "{}",
			}
			return stubSubscriberColumns(), [][]driver.Value{row}, nil
		}
		return nil, nil, errors.New("unexpected query: " + query)
	})

	hooks := &captureDispatcher{}
	svc := NewSubscriberService(queries, hooks, NewEmailValidator(), nil)

	res, err := svc.BulkImport(context.Background(), projectID.String(), []ImportSubscriber{
		{Email: "ada@example.test", Name: "Ada"},
		{Email: "grace@example.test", Name: "Grace"},
		{Email: "dup@example.test", Name: "Dup"},
		{Email: "not-an-email", Name: "Bad"},
	}, ImportOptions{ValidateMX: false, ValidateDisposable: false})
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}
	if res.Imported != 2 || res.Duplicates != 1 || res.SyntaxInvalid != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}

	events := hooks.list()
	if len(events) != 2 {
		t.Fatalf("dispatched %d events, want 2: %+v", len(events), events)
	}
	for i, wantEmail := range []string{"ada@example.test", "grace@example.test"} {
		if events[i].Type != "subscriber.created" {
			t.Errorf("event[%d].Type = %q, want subscriber.created", i, events[i].Type)
		}
		data, _ := events[i].Data.(map[string]any)
		if data["email"] != wantEmail {
			t.Errorf("event[%d].Data[email] = %v, want %q", i, data["email"], wantEmail)
		}
	}
}

func TestBulkUpdateStatusUnsubscribedSuppressesAndDispatchesEvents(t *testing.T) {
	projectID := uuid.New()
	sub1 := uuid.New()
	sub2 := uuid.New()
	now := time.Now().UTC()

	var suppressedEmails []string
	var mu sync.Mutex

	_, queries := openStubQueries(t, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "UPDATE subscribers"):
			rows := [][]driver.Value{
				stubSubscriberValueRow(sub1, projectID, "ada@example.test", "Ada", "unsubscribed"),
				stubSubscriberValueRow(sub2, projectID, "grace@example.test", "Grace", "unsubscribed"),
			}
			return stubSubscriberColumns(), rows, nil
		case strings.Contains(query, "INSERT INTO suppressions"):
			email, _ := args[1].Value.(string)
			reason, _ := args[2].Value.(string)
			if reason != SuppressionReasonUnsubscribe {
				return nil, nil, errors.New("unexpected suppression reason: " + reason)
			}
			mu.Lock()
			suppressedEmails = append(suppressedEmails, email)
			mu.Unlock()
			cols := []string{"id", "project_id", "email_normalized", "reason", "source", "created_at", "last_seen_at"}
			row := []driver.Value{uuid.NewString(), projectID[:], email, reason, "manual subscriber status change", now, now}
			return cols, [][]driver.Value{row}, nil
		case strings.Contains(query, "FROM projects WHERE id = $1"):
			cols, rows := stubProjectRow(projectID)
			return cols, rows, nil
		case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "FROM suppressions"):
			email, _ := args[1].Value.(string)
			mu.Lock()
			isSuppressed := false
			for _, s := range suppressedEmails {
				if s == email {
					isSuppressed = true
					break
				}
			}
			mu.Unlock()
			return []string{"exists"}, [][]driver.Value{{isSuppressed}}, nil
		case strings.Contains(query, "INSERT INTO email_logs"):
			return []string{
				"id", "project_id", "subscriber_id", "template_id", "to_email", "subject",
				"status", "error", "sent_at", "opened_at", "clicked_at",
				"broadcast_id", "complained_at", "newsletter_id",
			}, [][]driver.Value{{
				uuid.NewString(), projectID[:], nil, nil, "ada@example.test", "Transactional",
				"suppressed", "suppressed", now, nil, nil,
				nil, nil, nil,
			}}, nil
		default:
			return nil, nil, errors.New("unexpected query: " + query)
		}
	})

	hooks := &captureDispatcher{}
	suppressions := NewSuppressionService(queries)
	svc := NewSubscriberService(queries, hooks, NewEmailValidator(), suppressions)

	err := svc.BulkUpdateStatus(context.Background(), projectID.String(), []string{sub1.String(), sub2.String()}, "unsubscribed")
	if err != nil {
		t.Fatalf("BulkUpdateStatus: %v", err)
	}

	mu.Lock()
	gotSuppressed := append([]string(nil), suppressedEmails...)
	mu.Unlock()
	if len(gotSuppressed) != 2 || gotSuppressed[0] != "ada@example.test" || gotSuppressed[1] != "grace@example.test" {
		t.Fatalf("suppressed emails = %v, want [ada@example.test grace@example.test]", gotSuppressed)
	}

	events := hooks.list()
	if len(events) != 2 {
		t.Fatalf("dispatched %d events, want 2: %+v", len(events), events)
	}
	for i, wantEmail := range []string{"ada@example.test", "grace@example.test"} {
		if events[i].Type != "subscriber.unsubscribed" {
			t.Errorf("event[%d].Type = %q, want subscriber.unsubscribed", i, events[i].Type)
		}
		data, _ := events[i].Data.(map[string]any)
		if data["email"] != wantEmail {
			t.Errorf("event[%d].Data[email] = %v, want %q", i, data["email"], wantEmail)
		}
	}

	emailSvc := NewEmailService(queries, staticPublicURL("https://mail.example.com"), "secret", nil, nil, suppressions)
	sendErr := emailSvc.SendDirect(WithSystemContext(context.Background()), projectID.String(), "ada@example.test", "Transactional", "<p>Hi</p>")
	if !errors.Is(sendErr, ErrRecipientSuppressed) {
		t.Fatalf("transactional SendDirect after bulk unsubscribe err = %v, want %v", sendErr, ErrRecipientSuppressed)
	}
}
