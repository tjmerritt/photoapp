package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps pgxpool and provides query helpers used by all handlers.
type Pool struct {
	*pgxpool.Pool
}

// New opens a connection pool to PostgreSQL using the given DSN.
func New(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Pool{pool}, nil
}

// RefreshEmojiCounts refreshes the materialised view used for fast emoji counts.
// Call this after any insert/delete in emoji_reactions.
func (p *Pool) RefreshEmojiCounts(ctx context.Context) error {
	_, err := p.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY emoji_counts`)
	return err
}
