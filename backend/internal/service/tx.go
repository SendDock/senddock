package service

import (
	"context"
	"database/sql"

	"github.com/senddock/senddock/internal/db"
)

func withTransaction(ctx context.Context, conn *sql.DB, queries *db.Queries, fn func(*db.Queries) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(queries.WithTx(tx)); err != nil {
		return err
	}

	return tx.Commit()
}
