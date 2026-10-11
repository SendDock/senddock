package service

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/arkhe-systems/senddock/internal/db"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/google/uuid"
)

func TestExtractBounceRecipientsMatchesAll5xxFallbackCodes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "552 over quota angle address",
			raw:  "Subject: Delivery Status Notification\n\n552 5.2.2 <quota@example.com>: Mailbox full\n",
			want: []string{"quota@example.com"},
		},
		{
			name: "555 syntax error bare address",
			raw:  "Subject: Undelivered Mail\n\nRemote server replied: 555 param@example.org parameter unrecognized\n",
			want: []string{"param@example.org"},
		},
		{
			name: "4xx temporary failure ignored",
			raw:  "Subject: Delayed Mail\n\n451 4.4.1 <temp@example.com>: Temporary lookup failure\n",
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBounceRecipients(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("extractBounceRecipients() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("extractBounceRecipients()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestProcessFetchedMessagesOnlyMarksRecognizedBouncesSeen(t *testing.T) {
	projectID := uuid.New()
	suppressed := []string{}
	markedBounced := []string{}

	_, queries := openStubQueries(t, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "INSERT INTO suppressions"):
			email, _ := args[1].Value.(string)
			suppressed = append(suppressed, email)
			return []string{"id", "project_id", "email", "reason", "source", "created_at"}, [][]driver.Value{{
				uuid.NewString(), projectID[:], email, "bounce", "imap dsn poll", time.Now().UTC(),
			}}, nil
		case strings.Contains(query, "UPDATE subscribers") && strings.Contains(query, "status = 'bounced'"):
			return nil, nil, nil
		case strings.Contains(query, "UPDATE email_logs") && strings.Contains(query, "status = 'bounced'"):
			email, _ := args[1].Value.(string)
			markedBounced = append(markedBounced, email)
			return nil, [][]driver.Value{{1}}, nil
		default:
			return nil, nil, nil
		}
	})

	poller := NewBounceIMAPPoller(queries, nil, NewSuppressionService(queries), "secret")
	msgs := []*imapclient.FetchMessageBuffer{
		{
			SeqNum: 1,
			BodySection: []imapclient.FetchBodySectionBuffer{
				{
					Section: &imap.FetchItemBodySection{},
					Bytes:   []byte("Subject: Out of office\n\nI am away from my desk until Monday.\n"),
				},
			},
		},
		{
			SeqNum: 2,
			BodySection: []imapclient.FetchBodySectionBuffer{
				{
					Section: &imap.FetchItemBodySection{},
					Bytes:   []byte("Subject: Undelivered Mail\n\n552 <bounced@example.com>: user over quota\n"),
				},
			},
		},
	}

	seenSet, processed := poller.processFetchedMessages(context.Background(), db.Project{ID: projectID}, msgs)
	if processed != 1 {
		t.Fatalf("expected 1 processed bounce message, got %d", processed)
	}
	if seenSet.Contains(1) {
		t.Fatalf("expected unrelated message seq 1 not to be marked seen, got %v", seenSet)
	}
	if !seenSet.Contains(2) {
		t.Fatalf("expected bounce message seq 2 to be marked seen, got %v", seenSet)
	}
	if len(suppressed) != 1 || suppressed[0] != "bounced@example.com" {
		t.Fatalf("expected bounced@example.com to be suppressed, got %v", suppressed)
	}
	if len(markedBounced) != 1 || markedBounced[0] != "bounced@example.com" {
		t.Fatalf("expected bounced@example.com log to be marked bounced, got %v", markedBounced)
	}
}
