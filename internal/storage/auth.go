package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/justar9/btick/internal/domain"
)

var errDuplicateAPIAccount = errors.New("duplicate api account")

func (db *DB) CreateAPIAccount(ctx context.Context, email, name, tier, apiKeyHash, apiKeyPrefix string) (*domain.APIAccount, error) {
	const q = `INSERT INTO api_accounts (
		account_id, email, name, tier, api_key_hash, api_key_prefix, active, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,TRUE,$7)
	RETURNING account_id, email, name, tier, api_key_prefix, active, created_at, COALESCE(last_used_at, '0001-01-01T00:00:00Z'::timestamptz)`

	now := time.Now().UTC()
	accountID := uuid.New()
	row := db.Pool.QueryRow(ctx, q, accountID, strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(name), tier, apiKeyHash, apiKeyPrefix, now)

	account := &domain.APIAccount{}
	if err := row.Scan(&account.AccountID, &account.Email, &account.Name, &account.Tier, &account.APIKeyPrefix, &account.Active, &account.CreatedAt, &account.LastUsedAt); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return nil, errDuplicateAPIAccount
		}
		return nil, err
	}
	return account, nil
}

func (db *DB) LookupAPIAccountByKeyHash(ctx context.Context, apiKeyHash string) (*domain.APIAccount, error) {
	const q = `SELECT account_id, email, name, tier, api_key_prefix, active, created_at, COALESCE(last_used_at, '0001-01-01T00:00:00Z'::timestamptz)
	FROM api_accounts
	WHERE api_key_hash = $1`

	account := &domain.APIAccount{}
	if err := db.Pool.QueryRow(ctx, q, apiKeyHash).Scan(&account.AccountID, &account.Email, &account.Name, &account.Tier, &account.APIKeyPrefix, &account.Active, &account.CreatedAt, &account.LastUsedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return account, nil
}

func (db *DB) TouchAPIAccountLastUsed(ctx context.Context, accountID string, usedAt time.Time) error {
	const q = `UPDATE api_accounts SET last_used_at = $2 WHERE account_id = $1`
	_, err := db.Pool.Exec(ctx, q, accountID, usedAt)
	return err
}
