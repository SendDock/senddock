-- +goose Up
-- Workspace-level actions (creating a workspace, adding or removing a member, changing a
-- role) belong to the workspace, not to any one project, so they were written with
-- project_id NULL — and the audit log filters by project, which made every one of them
-- impossible to read anywhere in the product.
--
-- workspace_id gives those rows the scope they always had in practice; the project_id
-- column keeps meaning what it meant for the actions that happen inside a project.
ALTER TABLE audit_log ADD COLUMN workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE;

CREATE INDEX audit_log_workspace_idx ON audit_log(workspace_id, created_at DESC);

-- +goose Down
DROP INDEX audit_log_workspace_idx;
ALTER TABLE audit_log DROP COLUMN workspace_id;
