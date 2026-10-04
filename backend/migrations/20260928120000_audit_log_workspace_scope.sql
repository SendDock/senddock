-- +goose Up
ALTER TABLE audit_log ADD COLUMN workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE;

CREATE INDEX audit_log_workspace_idx ON audit_log(workspace_id, created_at DESC);

-- +goose Down
DROP INDEX audit_log_workspace_idx;
ALTER TABLE audit_log DROP COLUMN workspace_id;
