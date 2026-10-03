package service

import (
	"context"
	"errors"
	"strings"

	"github.com/arkhe-systems/senddock/internal/db"
	"github.com/google/uuid"
)

type TemplateService struct {
	queries *db.Queries
}

func NewTemplateService(queries *db.Queries) *TemplateService {
	return &TemplateService{queries: queries}
}

// resolveTextBody fills in the plain-text part of a template. Every message ships a
// text/plain part next to its HTML, and the editor has no field for it, so a template
// saved without one gets it derived from its HTML. A value that was authored is kept as
// it is.
func resolveTextBody(htmlBody, textBody string) string {
	if strings.TrimSpace(textBody) != "" {
		return textBody
	}
	return htmlToText(htmlBody)
}

func (s *TemplateService) Create(ctx context.Context, projectID, name, subject, htmlBody, textBody, templateType string) (db.Template, error) {
	pid, err := uuid.Parse(projectID)
	if err != nil {
		return db.Template{}, errors.New("invalid project id")
	}

	return s.queries.CreateTemplate(ctx, db.CreateTemplateParams{
		ProjectID: pid,
		Name:      name,
		Subject:   subject,
		HtmlBody:  htmlBody,
		TextBody:  resolveTextBody(htmlBody, textBody),
		Type:      templateType,
	})
}

func (s *TemplateService) ListByProject(ctx context.Context, projectID string) ([]db.Template, error) {
	pid, err := uuid.Parse(projectID)
	if err != nil {
		return nil, errors.New("invalid project id")
	}

	return s.queries.ListTemplatesByProject(ctx, pid)
}

func (s *TemplateService) GetByID(ctx context.Context, templateID, projectID string) (db.Template, error) {
	tid, err := uuid.Parse(templateID)
	if err != nil {
		return db.Template{}, errors.New("invalid template id")
	}

	pid, err := uuid.Parse(projectID)
	if err != nil {
		return db.Template{}, errors.New("invalid project id")
	}

	return s.queries.GetTemplateByID(ctx, db.GetTemplateByIDParams{
		ID:        tid,
		ProjectID: pid,
	})
}

func (s *TemplateService) Update(ctx context.Context, templateID, projectID, name, subject, htmlBody, textBody string) (db.Template, error) {
	tid, err := uuid.Parse(templateID)
	if err != nil {
		return db.Template{}, errors.New("invalid template id")
	}

	pid, err := uuid.Parse(projectID)
	if err != nil {
		return db.Template{}, errors.New("invalid project id")
	}

	return s.queries.UpdateTemplate(ctx, db.UpdateTemplateParams{
		ID:        tid,
		ProjectID: pid,
		Name:      name,
		Subject:   subject,
		HtmlBody:  htmlBody,
		TextBody:  resolveTextBody(htmlBody, textBody),
	})
}

func (s *TemplateService) Delete(ctx context.Context, templateID, projectID string) error {
	tid, err := uuid.Parse(templateID)
	if err != nil {
		return errors.New("invalid template id")
	}

	pid, err := uuid.Parse(projectID)
	if err != nil {
		return errors.New("invalid project id")
	}

	return s.queries.DeleteTemplate(ctx, db.DeleteTemplateParams{
		ID:        tid,
		ProjectID: pid,
	})
}
