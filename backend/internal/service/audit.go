package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/arkhe-systems/senddock/internal/db"
	"github.com/google/uuid"
	"github.com/sqlc-dev/pqtype"
)

type AuditService struct {
	queries *db.Queries
}

func NewAuditService(queries *db.Queries) *AuditService {
	return &AuditService{queries: queries}
}

type AuditEntry struct {
	ProjectID   uuid.NullUUID
	WorkspaceID uuid.NullUUID
	UserID      uuid.NullUUID
	Action      string
	TargetType  string
	TargetID    string
	Metadata    map[string]any
	IPAddress   string
	UserAgent   string
}

func (s *AuditService) Log(ctx context.Context, entry AuditEntry) {
	if s == nil {
		return
	}
	meta := json.RawMessage("{}")
	if entry.Metadata != nil {
		if encoded, err := json.Marshal(entry.Metadata); err == nil {
			meta = encoded
		}
	}
	_, _ = s.queries.CreateAuditEntry(ctx, db.CreateAuditEntryParams{
		ProjectID:   entry.ProjectID,
		WorkspaceID: entry.WorkspaceID,
		UserID:      entry.UserID,
		Action:      entry.Action,
		TargetType:  nullIfEmpty(entry.TargetType),
		TargetID:    nullIfEmpty(entry.TargetID),
		Metadata:    meta,
		IpAddress:   parseInet(entry.IPAddress),
		UserAgent:   nullIfEmpty(entry.UserAgent),
	})
}

func (s *AuditService) LogFromRequest(r *http.Request, projectID, userID, action, targetType, targetID string, metadata map[string]any) {
	s.logFromRequest(r, projectID, "", userID, action, targetType, targetID, metadata)
}

// LogWorkspaceFromRequest records an action that belongs to a workspace rather than to a
// project: creating or renaming it, or adding, changing and removing one of its members.
// There is no project to file those under, and filing them under none is what made every one
// of them unreadable — the audit log is filtered by scope.
func (s *AuditService) LogWorkspaceFromRequest(r *http.Request, workspaceID, userID, action, targetType, targetID string, metadata map[string]any) {
	s.logFromRequest(r, "", workspaceID, userID, action, targetType, targetID, metadata)
}

func (s *AuditService) logFromRequest(r *http.Request, projectID, workspaceID, userID, action, targetType, targetID string, metadata map[string]any) {
	pid, _ := uuid.Parse(projectID)
	wid, _ := uuid.Parse(workspaceID)
	uid, _ := uuid.Parse(userID)
	s.Log(r.Context(), AuditEntry{
		ProjectID:   uuid.NullUUID{UUID: pid, Valid: pid != uuid.Nil},
		WorkspaceID: uuid.NullUUID{UUID: wid, Valid: wid != uuid.Nil},
		UserID:      uuid.NullUUID{UUID: uid, Valid: uid != uuid.Nil},
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    metadata,
		IPAddress:   clientIP(r),
		UserAgent:   r.UserAgent(),
	})
}

func nullIfEmpty(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func parseInet(s string) pqtype.Inet {
	if s == "" {
		return pqtype.Inet{}
	}
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return pqtype.Inet{}
	}
	mask := net.CIDRMask(32, 32)
	if ip.To4() == nil {
		mask = net.CIDRMask(128, 128)
	}
	return pqtype.Inet{IPNet: net.IPNet{IP: ip, Mask: mask}, Valid: true}
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}
