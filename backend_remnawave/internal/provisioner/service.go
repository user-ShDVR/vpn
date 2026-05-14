// Package provisioner mediates between the cabinet DB and the Remnawave panel.
//
// One СвязьОК user maps to exactly one Remnawave user. The Remnawave panel
// returns a single `subscriptionUrl` that fans out to whatever nodes/inbounds
// the user's active internal squads grant access to. There is no per-server
// row in the DB any more — Remnawave does the multi-node bookkeeping itself.
package provisioner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/remnawave"
)

type Service struct {
	db                *db.DB
	rw                *remnawave.Client
	defaultSquadUUID  string
}

func New(database *db.DB, rw *remnawave.Client) *Service {
	return &Service{db: database, rw: rw}
}

// SetDefaultSquadUUID makes every provisioned user a member of this squad
// in addition to plan-specific squads. Empty disables the behaviour.
func (s *Service) SetDefaultSquadUUID(u string) { s.defaultSquadUUID = strings.TrimSpace(u) }

// Provision creates (or refreshes) the Remnawave user for the given subscription.
// Returns the subscription URL the customer hands to their VPN client.
//
// If the user already has a Remnawave UUID stored, this PATCHes that record
// (so the rotated URL is preserved); otherwise it POSTs a new user.
func (s *Service) Provision(ctx context.Context, user *db.User, sub *db.Subscription) (string, error) {
	if !s.rw.Configured() {
		return "", fmt.Errorf("remnawave client not configured")
	}
	plan, err := s.db.GetPlanByID(ctx, sub.PlanID)
	if err != nil {
		return "", fmt.Errorf("get plan: %w", err)
	}

	squads := s.composeSquads(plan)
	trafficBytes := planTrafficBytes(plan)
	log.Printf("provision user=%s plan=%s squads=%v default=%q", user.ID, plan.Name, squads, s.defaultSquadUUID)

	if user.RemnawaveUUID != nil {
		req := remnawave.UpdateUserRequest{
			UUID:                 *user.RemnawaveUUID,
			Status:               remnawave.StatusActive,
			ExpireAt:             &sub.ExpiresAt,
			TrafficLimitBytes:    &trafficBytes,
			TrafficLimitStrategy: resetStrategyOrDefault(plan.ResetStrategy),
			ActiveInternalSquads: squads,
			HwidDeviceLimit:      &plan.MaxDevices,
		}
		u, err := s.rw.UpdateUser(ctx, req)
		if err != nil {
			return "", fmt.Errorf("remnawave update: %w", err)
		}
		if u.SubscriptionURL != "" {
			_ = s.db.UpdateSubscriptionURL(ctx, user.ID, u.SubscriptionURL)
		}
		if user.SubscriptionURL != nil {
			return *user.SubscriptionURL, nil
		}
		return u.SubscriptionURL, nil
	}

	req := remnawave.CreateUserRequest{
		Username:             usernameFor(user),
		Status:               remnawave.StatusActive,
		ExpireAt:             sub.ExpiresAt,
		TrafficLimitBytes:    trafficBytes,
		TrafficLimitStrategy: resetStrategyOrDefault(plan.ResetStrategy),
		Email:                user.Email,
		Description:          "СвязьОК · " + user.Email,
		HwidDeviceLimit:      plan.MaxDevices,
		ActiveInternalSquads: squads,
	}
	u, err := s.rw.CreateUser(ctx, req)
	if err != nil {
		return "", fmt.Errorf("remnawave create: %w", err)
	}
	if err := s.db.SetRemnawaveLink(ctx, user.ID, u.UUID, u.ShortUUID, u.SubscriptionURL); err != nil {
		return "", fmt.Errorf("save remnawave link: %w", err)
	}
	return u.SubscriptionURL, nil
}

// Deprovision deletes the user from Remnawave and clears the cached identifiers.
func (s *Service) Deprovision(ctx context.Context, userID uuid.UUID) error {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user.RemnawaveUUID == nil {
		return nil
	}
	if err := s.rw.DeleteUser(ctx, *user.RemnawaveUUID); err != nil {
		// User may already be gone — proceed clearing the link anyway.
	}
	return s.db.ClearRemnawaveLink(ctx, userID)
}

// ExtendUserSubscription bumps DB expiry by N days AND pushes the new expiry
// to Remnawave so the panel doesn't disconnect the user prematurely.
func (s *Service) ExtendUserSubscription(ctx context.Context, userID uuid.UUID, days int) error {
	if err := s.db.ExtendSubscription(ctx, userID, days); err != nil {
		return fmt.Errorf("db extend: %w", err)
	}
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user.RemnawaveUUID == nil {
		return nil
	}
	sub, err := s.db.GetActiveSubscription(ctx, userID)
	if err != nil {
		return nil
	}
	_, err = s.rw.UpdateUser(ctx, remnawave.UpdateUserRequest{
		UUID:     *user.RemnawaveUUID,
		ExpireAt: &sub.ExpiresAt,
		Status:   remnawave.StatusActive,
	})
	return err
}

// GetSubscriptionURL returns the cached subscription URL from DB (no HTTP).
func (s *Service) GetSubscriptionURL(ctx context.Context, userID uuid.UUID) (string, error) {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user.SubscriptionURL == nil {
		return "", nil
	}
	return *user.SubscriptionURL, nil
}

// RotateSubscription asks Remnawave to revoke the user's current URL and
// returns the freshly minted one (also persisted to DB).
func (s *Service) RotateSubscription(ctx context.Context, userID uuid.UUID) (string, error) {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user.RemnawaveUUID == nil {
		return "", fmt.Errorf("user has no remnawave link")
	}
	u, err := s.rw.RevokeSubscription(ctx, *user.RemnawaveUUID)
	if err != nil {
		return "", fmt.Errorf("revoke: %w", err)
	}
	if err := s.db.UpdateSubscriptionURL(ctx, userID, u.SubscriptionURL); err != nil {
		return "", fmt.Errorf("save url: %w", err)
	}
	return u.SubscriptionURL, nil
}

// GetTraffic returns (used, limit) bytes for the user. Limit 0 = unlimited.
func (s *Service) GetTraffic(ctx context.Context, userID uuid.UUID) (used, limit int64, err error) {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	if user.RemnawaveUUID == nil {
		return 0, 0, nil
	}
	u, err := s.rw.GetUser(ctx, *user.RemnawaveUUID)
	if err != nil {
		return 0, 0, err
	}
	return u.UsedBytes(), u.TrafficLimitBytes, nil
}

// GetRemnawaveUser fetches the live panel-side user record (squads, reset
// strategy, hwid limit, fresh traffic, …). Returns nil if user has no panel
// link yet.
func (s *Service) GetRemnawaveUser(ctx context.Context, userID uuid.UUID) (*remnawave.User, error) {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.RemnawaveUUID == nil {
		return nil, nil
	}
	return s.rw.GetUser(ctx, *user.RemnawaveUUID)
}

// ListUserDevices returns the HWID device list for the user, or nil if no
// panel link.
func (s *Service) ListUserDevices(ctx context.Context, userID uuid.UUID) ([]remnawave.Device, error) {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.RemnawaveUUID == nil {
		return nil, nil
	}
	return s.rw.ListDevices(ctx, *user.RemnawaveUUID)
}

// DeleteUserDevice revokes one HWID for the user.
func (s *Service) DeleteUserDevice(ctx context.Context, userID uuid.UUID, hwid string) error {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.RemnawaveUUID == nil {
		return fmt.Errorf("user has no remnawave link")
	}
	return s.rw.DeleteDevice(ctx, *user.RemnawaveUUID, hwid)
}

// ActivateFreePlanIfNone activates the cheapest paid plan for users with no
// active subscription. Idempotent — returns existing subscription if any.
func (s *Service) ActivateFreePlanIfNone(ctx context.Context, user *db.User) (*db.Subscription, string, error) {
	if existing, err := s.db.GetActiveSubscription(ctx, user.ID); err == nil && existing != nil {
		url, err := s.GetSubscriptionURL(ctx, user.ID)
		if err != nil || url == "" {
			url, err = s.Provision(ctx, user, existing)
			if err != nil {
				return existing, "", err
			}
		}
		return existing, url, nil
	}
	plans, err := s.db.ListPlans(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list plans: %w", err)
	}
	var pick *db.Plan
	for i := range plans {
		if pick == nil || plans[i].CostKopecks < pick.CostKopecks {
			pick = &plans[i]
		}
	}
	if pick == nil {
		return nil, "", fmt.Errorf("no plan seeded")
	}
	expires := time.Now().AddDate(0, 0, pick.DurationDays)
	sub, err := s.db.CreateSubscription(ctx, user.ID, pick.ID, expires)
	if err != nil {
		return nil, "", fmt.Errorf("create subscription: %w", err)
	}
	url, err := s.Provision(ctx, user, sub)
	if err != nil {
		return sub, "", err
	}
	return sub, url, nil
}

// --- helpers ---

func planSquads(p *db.Plan) []string {
	out := make([]string, 0, len(p.SquadUUIDs))
	for _, s := range p.SquadUUIDs {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resetStrategyOrDefault maps a Plan.ResetStrategy column value to the
// Remnawave constant, falling back to NO_RESET on empty/unknown so we never
// send an invalid string the panel will reject.
func resetStrategyOrDefault(s string) string {
	switch s {
	case remnawave.StrategyDay, remnawave.StrategyWeek, remnawave.StrategyMonth, remnawave.StrategyMonthRolling:
		return s
	}
	return remnawave.StrategyNoReset
}

// ResetUserTraffic zeroes the user's current-period traffic counter on the
// panel. Called on plan switch / new purchase so the new period starts at 0.
func (s *Service) ResetUserTraffic(ctx context.Context, userID uuid.UUID) error {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.RemnawaveUUID == nil {
		return nil
	}
	return s.rw.ResetTraffic(ctx, *user.RemnawaveUUID)
}

// AddTraffic bumps the user's Remnawave trafficLimitBytes by extraBytes.
// Used by the buy-extra-GB flow.
func (s *Service) AddTraffic(ctx context.Context, userID uuid.UUID, extraBytes int64) error {
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.RemnawaveUUID == nil {
		return fmt.Errorf("user has no remnawave link")
	}
	u, err := s.rw.GetUser(ctx, *user.RemnawaveUUID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	newLimit := u.TrafficLimitBytes + extraBytes
	_, err = s.rw.UpdateUser(ctx, remnawave.UpdateUserRequest{
		UUID:              *user.RemnawaveUUID,
		TrafficLimitBytes: &newLimit,
	})
	return err
}

// composeSquads returns plan-specific squads plus the configured default
// squad (deduped). Order: defaultSquadUUID first, then plan squads.
func (s *Service) composeSquads(p *db.Plan) []string {
	planSet := planSquads(p)
	if s.defaultSquadUUID == "" {
		return planSet
	}
	out := make([]string, 0, len(planSet)+1)
	out = append(out, s.defaultSquadUUID)
	for _, sq := range planSet {
		if sq != s.defaultSquadUUID {
			out = append(out, sq)
		}
	}
	return out
}

func planTrafficBytes(p *db.Plan) int64 {
	if p.TrafficLimitGB == nil {
		return 0
	}
	return int64(*p.TrafficLimitGB) * 1024 * 1024 * 1024
}

func usernameFor(user *db.User) string {
	// Remnawave usernames must be unique. Use the first 16 chars of the user
	// UUID — short, stable, no PII leak.
	return strings.ReplaceAll(user.ID.String()[:18], "-", "")
}
