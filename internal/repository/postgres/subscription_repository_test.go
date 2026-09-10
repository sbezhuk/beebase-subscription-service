package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	repopostgres "github.com/sbezhuk/beebase-subscription-service/internal/repository/postgres"
)

// Ensure SubscriptionRepository satisfies the domain interface at compile time.
var _ subscription.Repository = (*repopostgres.SubscriptionRepository)(nil)

type mockQuerier struct {
	execFn     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	queryFn    func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (m *mockQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if m.execFn != nil {
		return m.execFn(ctx, sql, args...)
	}
	return pgconn.NewCommandTag(""), nil
}

func (m *mockQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, sql, args...)
	}
	return nil, nil
}

func (m *mockQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if m.queryRowFn != nil {
		return m.queryRowFn(ctx, sql, args...)
	}
	return nil
}

func TestSubscriptionRepository_RecordEventIfNotExists_Unit(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh event returns inserted = true", func(t *testing.T) {
		mock := &mockQuerier{
			execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				return pgconn.NewCommandTag("INSERT 0 1"), nil
			},
		}

		repo := repopostgres.NewSubscriptionRepository(mock)
		event := subscription.NewEvent(subscription.ProviderApple, "evt_123", "DID_RENEW", []byte(`{}`))

		inserted, err := repo.RecordEventIfNotExists(ctx, event)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !inserted {
			t.Errorf("expected inserted = true, got false")
		}
	})

	t.Run("duplicate event returns inserted = false without error", func(t *testing.T) {
		mock := &mockQuerier{
			execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				// ON CONFLICT DO NOTHING affects 0 rows
				return pgconn.NewCommandTag("INSERT 0 0"), nil
			},
		}

		repo := repopostgres.NewSubscriptionRepository(mock)
		event := subscription.NewEvent(subscription.ProviderApple, "evt_123", "DID_RENEW", []byte(`{}`))

		inserted, err := repo.RecordEventIfNotExists(ctx, event)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if inserted {
			t.Errorf("expected inserted = false for duplicate event, got true")
		}
	})

	t.Run("db failure returns error", func(t *testing.T) {
		dbErr := errors.New("connection failed")
		mock := &mockQuerier{
			execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				return pgconn.CommandTag{}, dbErr
			},
		}

		repo := repopostgres.NewSubscriptionRepository(mock)
		event := subscription.NewEvent(subscription.ProviderApple, "evt_123", "DID_RENEW", []byte(`{}`))

		inserted, err := repo.RecordEventIfNotExists(ctx, event)
		if !errors.Is(err, dbErr) {
			t.Errorf("expected error %v, got %v", dbErr, err)
		}
		if inserted {
			t.Errorf("expected inserted = false on error")
		}
	})
}

func TestSubscriptionRepository_Update_Unit(t *testing.T) {
	ctx := context.Background()

	t.Run("successful update", func(t *testing.T) {
		mock := &mockQuerier{
			execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				return pgconn.NewCommandTag("UPDATE 1"), nil
			},
		}

		repo := repopostgres.NewSubscriptionRepository(mock)
		sub := &subscription.Subscription{
			ID:        uuid.New(),
			ProductID: "beebase_pro_monthly",
			Status:    subscription.StatusActive,
			UpdatedAt: time.Now().UTC(),
		}

		err := repo.Update(ctx, sub)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("not found when 0 rows affected", func(t *testing.T) {
		mock := &mockQuerier{
			execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				return pgconn.NewCommandTag("UPDATE 0"), nil
			},
		}

		repo := repopostgres.NewSubscriptionRepository(mock)
		sub := &subscription.Subscription{
			ID:        uuid.New(),
			ProductID: "beebase_pro_monthly",
			Status:    subscription.StatusActive,
			UpdatedAt: time.Now().UTC(),
		}

		err := repo.Update(ctx, sub)
		if !errors.Is(err, subscription.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}
