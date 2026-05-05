package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func RandomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type DB struct {
	*sqlx.DB
}

func New(dsn string) (*DB, error) {
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &DB{db}, nil
}

func RunMigrations(dsn, migrationsPath string) error {
	m, err := migrate.New("file://"+migrationsPath, dsn)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// Models

type User struct {
	ID                      uuid.UUID  `db:"id" json:"id"`
	Email                   string     `db:"email" json:"email"`
	PasswordHash            string     `db:"password_hash" json:"-"`
	Role                    string     `db:"role" json:"role"`
	ReferralCode            *string    `db:"referral_code" json:"referral_code,omitempty"`
	SubscriptionToken       string     `db:"subscription_token" json:"-"`
	BalanceKopecks          int64      `db:"balance_kopecks" json:"balance_kopecks"`
	EmailVerified           bool       `db:"email_verified" json:"email_verified"`
	EmailVerifyToken        *string    `db:"email_verify_token" json:"-"`
	EmailVerifyExpiresAt    *time.Time `db:"email_verify_expires_at" json:"-"`
	PasswordResetToken      *string    `db:"password_reset_token" json:"-"`
	PasswordResetExpiresAt  *time.Time `db:"password_reset_expires_at" json:"-"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
}

type Transaction struct {
	ID                    uuid.UUID  `db:"id" json:"id"`
	UserID                uuid.UUID  `db:"user_id" json:"user_id"`
	AmountKopecks         int64      `db:"amount_kopecks" json:"amount_kopecks"`
	Type                  string     `db:"type" json:"type"`
	Description           *string    `db:"description" json:"description,omitempty"`
	RelatedSubscriptionID *uuid.UUID `db:"related_subscription_id" json:"related_subscription_id,omitempty"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
}

type PromoCode struct {
	Code      string     `db:"code" json:"code"`
	Kind      string     `db:"kind" json:"kind"`
	Value     int64      `db:"value" json:"value"`
	MaxUses   *int       `db:"max_uses" json:"max_uses,omitempty"`
	UsedCount int        `db:"used_count" json:"used_count"`
	ExpiresAt *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	IsActive  bool       `db:"is_active" json:"is_active"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
}

type PromoRedemption struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	UserID          uuid.UUID  `db:"user_id" json:"user_id"`
	Code            string     `db:"code" json:"code"`
	SubscriptionID  *uuid.UUID `db:"subscription_id" json:"subscription_id,omitempty"`
	DiscountKopecks int64      `db:"discount_kopecks" json:"discount_kopecks"`
	BonusDays       int        `db:"bonus_days" json:"bonus_days"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
}

type Payment struct {
	ID            uuid.UUID  `db:"id" json:"id"`
	UserID        uuid.UUID  `db:"user_id" json:"user_id"`
	Provider      string     `db:"provider" json:"provider"`
	BillID        string     `db:"bill_id" json:"bill_id"`
	AmountKopecks int64      `db:"amount_kopecks" json:"amount_kopecks"`
	Currency      string     `db:"currency" json:"currency"`
	Status        string     `db:"status" json:"status"`
	LinkURL       *string    `db:"link_url" json:"link_url,omitempty"`
	PayURL        *string    `db:"pay_url" json:"pay_url,omitempty"`
	Custom        *string    `db:"custom" json:"custom,omitempty"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
	PaidAt        *time.Time `db:"paid_at" json:"paid_at,omitempty"`
}

type Referral struct {
	ID         uuid.UUID `db:"id" json:"id"`
	ReferrerID uuid.UUID `db:"referrer_id" json:"referrer_id"`
	ReferredID uuid.UUID `db:"referred_id" json:"referred_id"`
	BonusDays  int       `db:"bonus_days" json:"bonus_days"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

type Plan struct {
	ID             uuid.UUID `db:"id" json:"id"`
	Name           string    `db:"name" json:"name"`
	DurationDays   int       `db:"duration_days" json:"duration_days"`
	TrafficLimitGB *int      `db:"traffic_limit_gb" json:"traffic_limit_gb"`
	MaxDevices     int       `db:"max_devices" json:"max_devices"`
	CostKopecks    int64     `db:"cost_kopecks" json:"cost_kopecks"`
	Description    string    `db:"description" json:"description"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
}

type Subscription struct {
	ID        uuid.UUID `db:"id" json:"id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	PlanID    uuid.UUID `db:"plan_id" json:"plan_id"`
	StartsAt  time.Time `db:"starts_at" json:"starts_at"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
	IsActive  bool      `db:"is_active" json:"is_active"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Server struct {
	ID         uuid.UUID `db:"id" json:"id"`
	Name       string    `db:"name" json:"name"`
	PanelURL   string    `db:"panel_url" json:"panel_url"`
	PanelUser  string    `db:"panel_user" json:"panel_user"`
	PanelPass  string    `db:"panel_pass" json:"panel_pass"`
	InboundID  int       `db:"inbound_id" json:"inbound_id"`
	Type       string    `db:"type" json:"type"`
	Host       string    `db:"host" json:"host"`
	Port       int       `db:"port" json:"port"`
	SubURL      string `db:"sub_url" json:"sub_url"`
	SubPath     string `db:"sub_path" json:"sub_path"`
	IsActive    bool   `db:"is_active" json:"is_active"`
	MaxClients  int    `db:"max_clients" json:"max_clients"`
	ClientCount int    `db:"client_count" json:"client_count"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

type ServerClient struct {
	ID          uuid.UUID `db:"id" json:"id"`
	UserID      uuid.UUID `db:"user_id" json:"user_id"`
	ServerID    uuid.UUID `db:"server_id" json:"server_id"`
	ClientUUID  uuid.UUID `db:"client_uuid" json:"client_uuid"`
	XrayEmail   string    `db:"xray_email" json:"xray_email"`
	SubID       string    `db:"sub_id" json:"sub_id"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// Queries

func (d *DB) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	token, err := RandomToken(24)
	if err != nil {
		return nil, fmt.Errorf("gen subscription token: %w", err)
	}
	var u User
	err = d.QueryRowxContext(ctx,
		`INSERT INTO users (email, password_hash, subscription_token) VALUES ($1, $2, $3) RETURNING *`,
		email, passwordHash, token,
	).StructScan(&u)
	return &u, err
}

func (d *DB) GetUserBySubscriptionToken(ctx context.Context, token string) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM users WHERE subscription_token = $1`, token,
	).StructScan(&u)
	return &u, err
}

func (d *DB) RotateSubscriptionToken(ctx context.Context, userID uuid.UUID) (string, error) {
	token, err := RandomToken(24)
	if err != nil {
		return "", err
	}
	_, err = d.ExecContext(ctx,
		`UPDATE users SET subscription_token = $1 WHERE id = $2`,
		token, userID,
	)
	return token, err
}

// --- Email verification ---

func (d *DB) SetEmailVerifyToken(ctx context.Context, userID uuid.UUID, token string, expiresAt time.Time) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET email_verify_token = $1, email_verify_expires_at = $2 WHERE id = $3`,
		token, expiresAt, userID,
	)
	return err
}

func (d *DB) GetUserByEmailVerifyToken(ctx context.Context, token string) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM users WHERE email_verify_token = $1 AND email_verify_expires_at > NOW()`,
		token,
	).StructScan(&u)
	return &u, err
}

func (d *DB) MarkEmailVerified(ctx context.Context, userID uuid.UUID) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET email_verified = TRUE, email_verify_token = NULL, email_verify_expires_at = NULL WHERE id = $1`,
		userID,
	)
	return err
}

// --- Password reset ---

func (d *DB) SetPasswordResetToken(ctx context.Context, userID uuid.UUID, token string, expiresAt time.Time) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET password_reset_token = $1, password_reset_expires_at = $2 WHERE id = $3`,
		token, expiresAt, userID,
	)
	return err
}

func (d *DB) GetUserByPasswordResetToken(ctx context.Context, token string) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM users WHERE password_reset_token = $1 AND password_reset_expires_at > NOW()`,
		token,
	).StructScan(&u)
	return &u, err
}

func (d *DB) ClearPasswordResetToken(ctx context.Context, userID uuid.UUID) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET password_reset_token = NULL, password_reset_expires_at = NULL WHERE id = $1`,
		userID,
	)
	return err
}

func (d *DB) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx, `SELECT * FROM users WHERE email = $1`, email).StructScan(&u)
	return &u, err
}

func (d *DB) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx, `SELECT * FROM users WHERE id = $1`, id).StructScan(&u)
	return &u, err
}

func (d *DB) ListPlans(ctx context.Context) ([]Plan, error) {
	var plans []Plan
	err := d.SelectContext(ctx, &plans, `SELECT * FROM plans ORDER BY duration_days`)
	return plans, err
}

func (d *DB) GetPlanByID(ctx context.Context, id uuid.UUID) (*Plan, error) {
	var p Plan
	err := d.QueryRowxContext(ctx, `SELECT * FROM plans WHERE id = $1`, id).StructScan(&p)
	return &p, err
}

func (d *DB) CreatePlan(ctx context.Context, name string, durationDays int, trafficGB *int, maxDevices int, costKopecks int64, description string) (*Plan, error) {
	var p Plan
	err := d.QueryRowxContext(ctx,
		`INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, description) VALUES ($1, $2, $3, $4, $5, $6) RETURNING *`,
		name, durationDays, trafficGB, maxDevices, costKopecks, description,
	).StructScan(&p)
	return &p, err
}

func (d *DB) GetActiveSubscription(ctx context.Context, userID uuid.UUID) (*Subscription, error) {
	var s Subscription
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM subscriptions WHERE user_id = $1 AND is_active = TRUE AND expires_at > NOW() ORDER BY expires_at DESC LIMIT 1`,
		userID,
	).StructScan(&s)
	return &s, err
}

func (d *DB) CreateSubscription(ctx context.Context, userID, planID uuid.UUID, expiresAt time.Time) (*Subscription, error) {
	var s Subscription
	err := d.QueryRowxContext(ctx,
		`INSERT INTO subscriptions (user_id, plan_id, expires_at) VALUES ($1, $2, $3) RETURNING *`,
		userID, planID, expiresAt,
	).StructScan(&s)
	return &s, err
}

func (d *DB) ExpireSubscription(ctx context.Context, id uuid.UUID) error {
	_, err := d.ExecContext(ctx, `UPDATE subscriptions SET is_active = FALSE WHERE id = $1`, id)
	return err
}

func (d *DB) ListExpiredActiveSubscriptions(ctx context.Context) ([]Subscription, error) {
	var subs []Subscription
	err := d.SelectContext(ctx, &subs,
		`SELECT * FROM subscriptions WHERE is_active = TRUE AND expires_at <= NOW()`,
	)
	return subs, err
}

func (d *DB) GetEntryServer(ctx context.Context) (*Server, error) {
	var s Server
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM servers WHERE type = 'entry' AND is_active = TRUE LIMIT 1`,
	).StructScan(&s)
	return &s, err
}

func (d *DB) GetLeastLoadedEntryServer(ctx context.Context) (*Server, error) {
	var s Server
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM servers WHERE type = 'entry' AND is_active = TRUE AND client_count < max_clients ORDER BY client_count ASC LIMIT 1`,
	).StructScan(&s)
	return &s, err
}

func (d *DB) GetTopNLeastLoadedEntryServers(ctx context.Context, n int) ([]Server, error) {
	var servers []Server
	err := d.SelectContext(ctx, &servers,
		`SELECT * FROM servers WHERE type = 'entry' AND is_active = TRUE AND client_count < max_clients ORDER BY client_count ASC LIMIT $1`,
		n,
	)
	return servers, err
}

func (d *DB) IncrementServerClientCount(ctx context.Context, serverID uuid.UUID) error {
	_, err := d.ExecContext(ctx,
		`UPDATE servers SET client_count = client_count + 1 WHERE id = $1`,
		serverID,
	)
	return err
}

func (d *DB) DecrementServerClientCount(ctx context.Context, serverID uuid.UUID) error {
	_, err := d.ExecContext(ctx,
		`UPDATE servers SET client_count = GREATEST(client_count - 1, 0) WHERE id = $1`,
		serverID,
	)
	return err
}

func (d *DB) UpdateServer(ctx context.Context, id uuid.UUID, isActive *bool, maxClients *int) error {
	if isActive != nil {
		if _, err := d.ExecContext(ctx, `UPDATE servers SET is_active = $1 WHERE id = $2`, *isActive, id); err != nil {
			return err
		}
	}
	if maxClients != nil {
		if _, err := d.ExecContext(ctx, `UPDATE servers SET max_clients = $1 WHERE id = $2`, *maxClients, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) SyncServerClientCounts(ctx context.Context) error {
	_, err := d.ExecContext(ctx,
		`UPDATE servers SET client_count = (SELECT COUNT(*) FROM server_clients WHERE server_clients.server_id = servers.id)`,
	)
	return err
}

func (d *DB) GetServerByID(ctx context.Context, id uuid.UUID) (*Server, error) {
	var s Server
	err := d.QueryRowxContext(ctx, `SELECT * FROM servers WHERE id = $1`, id).StructScan(&s)
	return &s, err
}

func (d *DB) GetServerClientsByServer(ctx context.Context, serverID uuid.UUID) ([]ServerClient, error) {
	var clients []ServerClient
	err := d.SelectContext(ctx, &clients,
		`SELECT * FROM server_clients WHERE server_id = $1`,
		serverID,
	)
	return clients, err
}

func (d *DB) UpdateServerClient(ctx context.Context, id, newServerID, newClientUUID uuid.UUID, newSubID string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE server_clients SET server_id = $1, client_uuid = $2, sub_id = $3 WHERE id = $4`,
		newServerID, newClientUUID, newSubID, id,
	)
	return err
}

func (d *DB) ListServers(ctx context.Context) ([]Server, error) {
	var servers []Server
	err := d.SelectContext(ctx, &servers, `SELECT * FROM servers ORDER BY type`)
	return servers, err
}

func (d *DB) CreateServer(ctx context.Context, s Server) (*Server, error) {
	var result Server
	err := d.QueryRowxContext(ctx,
		`INSERT INTO servers (name, panel_url, panel_user, panel_pass, inbound_id, type, host, port, sub_url, sub_path)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING *`,
		s.Name, s.PanelURL, s.PanelUser, s.PanelPass, s.InboundID, s.Type, s.Host, s.Port, s.SubURL, s.SubPath,
	).StructScan(&result)
	return &result, err
}

func (d *DB) GetServerClientByUserAndServer(ctx context.Context, userID, serverID uuid.UUID) (*ServerClient, error) {
	var sc ServerClient
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM server_clients WHERE user_id = $1 AND server_id = $2`,
		userID, serverID,
	).StructScan(&sc)
	return &sc, err
}

func (d *DB) GetServerClientsByUser(ctx context.Context, userID uuid.UUID) ([]ServerClient, error) {
	var clients []ServerClient
	err := d.SelectContext(ctx, &clients,
		`SELECT * FROM server_clients WHERE user_id = $1`,
		userID,
	)
	return clients, err
}

func (d *DB) CreateServerClient(ctx context.Context, userID, serverID, clientUUID uuid.UUID, email, subID string) (*ServerClient, error) {
	var sc ServerClient
	err := d.QueryRowxContext(ctx,
		`INSERT INTO server_clients (user_id, server_id, client_uuid, xray_email, sub_id) VALUES ($1, $2, $3, $4, $5) RETURNING *`,
		userID, serverID, clientUUID, email, subID,
	).StructScan(&sc)
	return &sc, err
}

func (d *DB) DeleteServerClientsByUser(ctx context.Context, userID uuid.UUID) error {
	_, err := d.ExecContext(ctx, `DELETE FROM server_clients WHERE user_id = $1`, userID)
	return err
}

func (d *DB) ListUsers(ctx context.Context, limit, offset int) ([]User, error) {
	var users []User
	err := d.SelectContext(ctx, &users,
		`SELECT * FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	return users, err
}

// --- Referrals ---

func (d *DB) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`,
		passwordHash, userID,
	)
	return err
}

func (d *DB) SetReferralCode(ctx context.Context, userID uuid.UUID, code string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET referral_code = $1 WHERE id = $2`,
		code, userID,
	)
	return err
}

func (d *DB) GetUserByReferralCode(ctx context.Context, code string) (*User, error) {
	var u User
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM users WHERE referral_code = $1`, code,
	).StructScan(&u)
	return &u, err
}

func (d *DB) CreateReferral(ctx context.Context, referrerID, referredID uuid.UUID, bonusDays int) (*Referral, error) {
	var r Referral
	err := d.QueryRowxContext(ctx,
		`INSERT INTO referrals (referrer_id, referred_id, bonus_days) VALUES ($1, $2, $3) RETURNING *`,
		referrerID, referredID, bonusDays,
	).StructScan(&r)
	return &r, err
}

func (d *DB) GetReferralByReferred(ctx context.Context, referredID uuid.UUID) (*Referral, error) {
	var r Referral
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM referrals WHERE referred_id = $1`, referredID,
	).StructScan(&r)
	return &r, err
}

func (d *DB) CountReferrals(ctx context.Context, referrerID uuid.UUID) (int, error) {
	var count int
	err := d.QueryRowxContext(ctx,
		`SELECT COUNT(*) FROM referrals WHERE referrer_id = $1`, referrerID,
	).Scan(&count)
	return count, err
}

func (d *DB) DeactivateUserSubscriptions(ctx context.Context, userID uuid.UUID) error {
	_, err := d.ExecContext(ctx,
		`UPDATE subscriptions SET is_active = FALSE WHERE user_id = $1 AND is_active = TRUE`,
		userID,
	)
	return err
}

func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := d.QueryRowxContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (d *DB) CountActiveSubscriptions(ctx context.Context) (int, error) {
	var count int
	err := d.QueryRowxContext(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE is_active = TRUE AND expires_at > NOW()`,
	).Scan(&count)
	return count, err
}

func (d *DB) CountServers(ctx context.Context) (int, error) {
	var count int
	err := d.QueryRowxContext(ctx, `SELECT COUNT(*) FROM servers WHERE is_active = TRUE`).Scan(&count)
	return count, err
}

func (d *DB) ExtendSubscription(ctx context.Context, userID uuid.UUID, days int) error {
	_, err := d.ExecContext(ctx,
		`UPDATE subscriptions SET expires_at = expires_at + ($1 || ' days')::INTERVAL
		 WHERE user_id = $2 AND is_active = TRUE AND expires_at > NOW()`,
		fmt.Sprintf("%d", days), userID,
	)
	return err
}

// --- Balance ledger ---

var ErrInsufficientBalance = fmt.Errorf("insufficient balance")

// CreditBalance adds amount (positive kopecks) and records a transaction. Atomic.
func (d *DB) CreditBalance(ctx context.Context, userID uuid.UUID, amount int64, txType, description string, relatedSubID *uuid.UUID) (*Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("credit amount must be positive")
	}
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET balance_kopecks = balance_kopecks + $1 WHERE id = $2`,
		amount, userID,
	); err != nil {
		return nil, err
	}
	var t Transaction
	if err := tx.QueryRowxContext(ctx,
		`INSERT INTO transactions (user_id, amount_kopecks, type, description, related_subscription_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING *`,
		userID, amount, txType, description, relatedSubID,
	).StructScan(&t); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t, nil
}

// DebitBalance subtracts amount and records a transaction. Atomic. Errors with ErrInsufficientBalance if balance < amount.
func (d *DB) DebitBalance(ctx context.Context, userID uuid.UUID, amount int64, txType, description string, relatedSubID *uuid.UUID) (*Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("debit amount must be positive")
	}
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET balance_kopecks = balance_kopecks - $1 WHERE id = $2 AND balance_kopecks >= $1`,
		amount, userID,
	)
	if err != nil {
		return nil, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, ErrInsufficientBalance
	}
	var t Transaction
	if err := tx.QueryRowxContext(ctx,
		`INSERT INTO transactions (user_id, amount_kopecks, type, description, related_subscription_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING *`,
		userID, -amount, txType, description, relatedSubID,
	).StructScan(&t); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) GetTransactionsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]Transaction, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var txs []Transaction
	err := d.SelectContext(ctx, &txs,
		`SELECT * FROM transactions WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit,
	)
	return txs, err
}

// --- Promo codes ---

var ErrPromoNotFound = fmt.Errorf("promo code not found")
var ErrPromoExhausted = fmt.Errorf("promo code exhausted")
var ErrPromoExpired = fmt.Errorf("promo code expired")
var ErrPromoAlreadyRedeemed = fmt.Errorf("promo code already redeemed by user")

func (d *DB) GetPromoCode(ctx context.Context, code string) (*PromoCode, error) {
	var p PromoCode
	err := d.QueryRowxContext(ctx, `SELECT * FROM promo_codes WHERE code = $1 AND is_active`, code).StructScan(&p)
	return &p, err
}

// RedeemPromo validates and atomically consumes a promo code for a user. Errors:
// ErrPromoNotFound, ErrPromoExpired, ErrPromoExhausted, ErrPromoAlreadyRedeemed.
// Returns the loaded PromoCode (caller computes discount/bonus) and creates a
// redemption row tied to subID (may be nil).
func (d *DB) RedeemPromo(ctx context.Context, userID uuid.UUID, code string, subID *uuid.UUID, discountKopecks int64, bonusDays int) (*PromoCode, error) {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var p PromoCode
	if err := tx.QueryRowxContext(ctx,
		`SELECT * FROM promo_codes WHERE code = $1 AND is_active FOR UPDATE`, code,
	).StructScan(&p); err != nil {
		return nil, ErrPromoNotFound
	}
	if p.ExpiresAt != nil && p.ExpiresAt.Before(time.Now()) {
		return nil, ErrPromoExpired
	}
	if p.MaxUses != nil && p.UsedCount >= *p.MaxUses {
		return nil, ErrPromoExhausted
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE promo_codes SET used_count = used_count + 1 WHERE code = $1`, code,
	); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO promo_redemptions (user_id, code, subscription_id, discount_kopecks, bonus_days)
		 VALUES ($1, $2, $3, $4, $5)`,
		userID, code, subID, discountKopecks, bonusDays,
	); err != nil {
		// Likely UNIQUE(user_id, code) violation
		return nil, ErrPromoAlreadyRedeemed
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *DB) CreatePromoCode(ctx context.Context, p PromoCode) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO promo_codes (code, kind, value, max_uses, expires_at, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		p.Code, p.Kind, p.Value, p.MaxUses, p.ExpiresAt, p.IsActive,
	)
	return err
}

func (d *DB) ListPromoCodes(ctx context.Context) ([]PromoCode, error) {
	var out []PromoCode
	err := d.SelectContext(ctx, &out, `SELECT * FROM promo_codes ORDER BY created_at DESC`)
	return out, err
}

// --- Helpers for cabinet ---

type ReferralWithEmail struct {
	Referral
	Email string `db:"email" json:"email"`
}

func (d *DB) ListReferralsByReferrer(ctx context.Context, referrerID uuid.UUID) ([]ReferralWithEmail, error) {
	var refs []ReferralWithEmail
	err := d.SelectContext(ctx, &refs,
		`SELECT r.*, u.email FROM referrals r JOIN users u ON u.id = r.referred_id WHERE r.referrer_id = $1 ORDER BY r.created_at DESC`,
		referrerID,
	)
	return refs, err
}

// --- Payments ---

func (d *DB) CreatePayment(ctx context.Context, p Payment) (*Payment, error) {
	var out Payment
	err := d.QueryRowxContext(ctx,
		`INSERT INTO payments (user_id, provider, bill_id, amount_kopecks, currency, status, link_url, pay_url, custom)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *`,
		p.UserID, p.Provider, p.BillID, p.AmountKopecks, p.Currency, p.Status, p.LinkURL, p.PayURL, p.Custom,
	).StructScan(&out)
	return &out, err
}

func (d *DB) GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error) {
	var p Payment
	err := d.QueryRowxContext(ctx, `SELECT * FROM payments WHERE id = $1`, id).StructScan(&p)
	return &p, err
}

func (d *DB) GetPaymentByBillID(ctx context.Context, provider, billID string) (*Payment, error) {
	var p Payment
	err := d.QueryRowxContext(ctx,
		`SELECT * FROM payments WHERE provider = $1 AND bill_id = $2`,
		provider, billID,
	).StructScan(&p)
	return &p, err
}

func (d *DB) UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status string, paid bool) error {
	if paid {
		_, err := d.ExecContext(ctx,
			`UPDATE payments SET status = $1, paid_at = NOW() WHERE id = $2 AND status != 'success'`,
			status, id,
		)
		return err
	}
	_, err := d.ExecContext(ctx,
		`UPDATE payments SET status = $1 WHERE id = $2`,
		status, id,
	)
	return err
}

func (d *DB) ListPaymentsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]Payment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var ps []Payment
	err := d.SelectContext(ctx, &ps,
		`SELECT * FROM payments WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit,
	)
	return ps, err
}

type ServerClientWithName struct {
	ServerClient
	ServerName string `db:"server_name" json:"server_name"`
}

func (d *DB) GetServerClientsWithNames(ctx context.Context, userID uuid.UUID) ([]ServerClientWithName, error) {
	var rows []ServerClientWithName
	err := d.SelectContext(ctx, &rows,
		`SELECT sc.*, s.name AS server_name FROM server_clients sc JOIN servers s ON s.id = sc.server_id WHERE sc.user_id = $1`,
		userID,
	)
	return rows, err
}
