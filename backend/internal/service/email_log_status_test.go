package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/senddock/senddock/internal/db"
)

func TestLogPendingDoesNotClaimTheSendSucceeded(t *testing.T) {
	conn, queries := leaseTestDB(t)
	ctx := context.Background()

	_, projectID, subscriberID := leaseFixtures(t, conn)
	svc := NewEmailService(queries, nil, "", nil, nil, nil)

	logID := svc.logPending(ctx, projectID,
		uuid.NullUUID{UUID: subscriberID, Valid: true}, uuid.NullUUID{},
		"recipient@example.test", "Subject",
		uuid.NullUUID{}, uuid.NullUUID{})
	if logID == uuid.Nil {
		t.Fatal("logPending returned no id")
	}

	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM email_logs WHERE id = $1`, logID).Scan(&status); err != nil {
		t.Fatalf("read log row: %v", err)
	}
	if status != "pending" {
		t.Fatalf("new log row status = %q, want %q", status, "pending")
	}

	sent, err := queries.CountEmailLogsByStatus(ctx, db.CountEmailLogsByStatusParams{
		ProjectID: projectID,
		Status:    "sent",
	})
	if err != nil {
		t.Fatalf("count sent logs: %v", err)
	}
	if sent != 0 {
		t.Fatalf("stats count %d sends for an unfinished attempt, want 0", sent)
	}
}

func TestMarkLogStatusSettlesTheOutcome(t *testing.T) {
	conn, queries := leaseTestDB(t)
	ctx := context.Background()

	_, projectID, subscriberID := leaseFixtures(t, conn)
	svc := NewEmailService(queries, nil, "", nil, nil, nil)

	logID := svc.logPending(ctx, projectID,
		uuid.NullUUID{UUID: subscriberID, Valid: true}, uuid.NullUUID{},
		"recipient@example.test", "Subject",
		uuid.NullUUID{}, uuid.NullUUID{})

	svc.markLogStatus(ctx, projectID, logID, "sent", nil)

	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM email_logs WHERE id = $1`, logID).Scan(&status); err != nil {
		t.Fatalf("read log row: %v", err)
	}
	if status != "sent" {
		t.Fatalf("settled status = %q, want %q", status, "sent")
	}

	sent, err := queries.CountEmailLogsByStatus(ctx, db.CountEmailLogsByStatusParams{
		ProjectID: projectID,
		Status:    "sent",
	})
	if err != nil {
		t.Fatalf("count sent logs: %v", err)
	}
	if sent != 1 {
		t.Fatalf("stats count %d sends after a settled one, want 1", sent)
	}
}
