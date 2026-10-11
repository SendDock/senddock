package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arkhe-systems/senddock/internal/db"
	"github.com/google/uuid"
)

func parseDriverUUID(v driver.Value) uuid.UUID {
	switch val := v.(type) {
	case uuid.UUID:
		return val
	case []byte:
		if u, err := uuid.FromBytes(val); err == nil {
			return u
		}
		if u, err := uuid.ParseBytes(val); err == nil {
			return u
		}
	case string:
		if u, err := uuid.Parse(val); err == nil {
			return u
		}
	}
	return uuid.Nil
}

func TestExecuteCampaignSettlesAlreadyCompletedBroadcast(t *testing.T) {
	projectID := uuid.New()
	campaignID := uuid.New()
	templateID := uuid.New()
	subscriberID := uuid.New()
	broadcastID := uuid.New()

	var linkedBroadcastID uuid.UUID
	var settledBroadcastID uuid.UUID
	callOrder := []string{}

	_, queries := openStubQueries(t, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM projects WHERE id = $1"):
			cols, rows := stubProjectRow(projectID)
			return cols, rows, nil
		case strings.Contains(query, "FROM templates WHERE id = $1"):
			cols, rows := stubTemplateRow(templateID, projectID, "email")
			return cols, rows, nil
		case strings.Contains(query, "FROM subscribers"):
			return stubSubscriberColumns(), [][]driver.Value{
				stubSubscriberValueRow(subscriberID, projectID, "sub@example.com", "Sub", "subscribed"),
			}, nil
		case strings.Contains(query, "INSERT INTO broadcasts"):
			return []string{
				"id", "project_id", "template_id", "subject", "variables",
				"status", "total_recipients", "sent_count", "failed_count", "suppressed_count",
				"started_at", "finished_at", "html_fields", "newsletter_id",
			}, [][]driver.Value{{
				broadcastID[:], projectID[:], templateID[:], "Subject", []byte(`{}`),
				"running", int64(1), int64(0), int64(0), int64(0),
				time.Now().UTC(), nil, []byte(`[]`), nil,
			}}, nil
		case strings.Contains(query, "INSERT INTO broadcast_jobs"):
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "UPDATE campaigns SET status = 'sending'"):
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "UPDATE campaigns SET broadcast_id"):
			callOrder = append(callOrder, "set_broadcast")
			linkedBroadcastID = parseDriverUUID(args[0].Value)
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "FROM broadcasts b") && strings.Contains(query, "AND b.id = $1") && strings.Contains(query, "AND b.status = 'completed'"):
			callOrder = append(callOrder, "mark_done")
			settledBroadcastID = parseDriverUUID(args[0].Value)
			return nil, [][]driver.Value{{1}}, nil
		default:
			return nil, nil, nil
		}
	})

	emailSvc := NewEmailService(queries, staticPublicURL("https://mail.example.com"), "secret", nil, nil, NewSuppressionService(queries))
	worker := NewCampaignWorker(queries, emailSvc)
	worker.ExecuteCampaign(context.Background(), db.Campaign{
		ID:         campaignID,
		ProjectID:  projectID,
		TemplateID: templateID,
		Name:       "Launch",
		Subject:    "Subject",
		Status:     "sending",
		Variables:  json.RawMessage(`{}`),
	})

	if len(callOrder) != 2 || callOrder[0] != "set_broadcast" || callOrder[1] != "mark_done" {
		t.Fatalf("expected set_broadcast followed by mark_done, got %v", callOrder)
	}
	if linkedBroadcastID == uuid.Nil || settledBroadcastID != linkedBroadcastID {
		t.Fatalf("expected MarkCampaignDoneFromBroadcast for %v, got %v", linkedBroadcastID, settledBroadcastID)
	}
}

func TestRecoverInProgressBroadcastsReconcilesCompletedCampaigns(t *testing.T) {
	reconciledBroadcasts := false
	reconciledCampaigns := false

	_, queries := openStubQueries(t, func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "UPDATE broadcasts b"):
			reconciledBroadcasts = true
			return nil, [][]driver.Value{{1}}, nil
		case strings.Contains(query, "UPDATE campaigns") && strings.Contains(query, "FROM broadcasts b") && !strings.Contains(query, "b.id = $1"):
			reconciledCampaigns = true
			return nil, [][]driver.Value{{1}}, nil
		default:
			return nil, nil, nil
		}
	})

	emailSvc := NewEmailService(queries, staticPublicURL("https://mail.example.com"), "secret", nil, nil, nil)
	if err := emailSvc.RecoverInProgressBroadcasts(context.Background()); err != nil {
		t.Fatalf("RecoverInProgressBroadcasts returned error: %v", err)
	}
	if !reconciledBroadcasts {
		t.Fatal("expected ReconcileStuckBroadcasts to run")
	}
	if !reconciledCampaigns {
		t.Fatal("expected ReconcileCompletedCampaigns to run")
	}
}
