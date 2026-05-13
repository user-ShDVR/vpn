// Package payment owns the Platega reconciliation poller.
//
// Platega has NO webhook with a signature, so we cannot trust a server-side
// push to credit balance. Instead, the cabinet polls each pending payment
// roughly every minute and asks Platega for its current status. On the first
// CONFIRMED status we credit the user's balance and mark the payment paid;
// on terminal failure we mark fail (no more polling).
//
// Payments older than maxAge (default 2h) are abandoned by the poller — they
// will simply stay 'pending' in DB. Lifecycle: the redirect handler on the
// cabinet may still credit them if the user happens to return to the page,
// since that path also runs the same check.
package payment

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/payment/platega"
)

// ReferralExtender abstracts provisioner.ExtendUserSubscription so the poller
// can credit a referrer without importing provisioner (avoids import cycle).
type ReferralExtender interface {
	ExtendUserSubscription(ctx context.Context, userID uuid.UUID, days int) error
}

type Poller struct {
	db       *db.DB
	platega  *platega.Client
	cron     *cron.Cron
	maxAge   time.Duration
	extender ReferralExtender
}

func NewPoller(database *db.DB, p *platega.Client) *Poller {
	return &Poller{
		db:      database,
		platega: p,
		cron:    cron.New(),
		maxAge:  2 * time.Hour,
	}
}

// SetReferralExtender wires in the provisioner so the poller can credit the
// referrer their bonus days on the referred user's first paid payment.
func (p *Poller) SetReferralExtender(e ReferralExtender) { p.extender = e }

func (p *Poller) Start() {
	if !p.platega.Configured() {
		slog.Info("payment poller skipped: platega not configured")
		return
	}
	_, _ = p.cron.AddFunc("@every 1m", p.tick)
	p.cron.Start()
	slog.Info("platega payment poller started")
}

func (p *Poller) Stop() { p.cron.Stop() }

func (p *Poller) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payments, err := p.db.ListPendingPayments(ctx, "platega", p.maxAge)
	if err != nil {
		slog.Error("payment poller: list pending", "err", err)
		return
	}
	for _, pm := range payments {
		if pm.BillID == "" {
			continue
		}
		p.reconcile(ctx, &pm)
	}
}

// Reconcile checks one payment's status against Platega and credits / fails it.
// Exported so the post-redirect handler can call the same logic synchronously.
func (p *Poller) Reconcile(ctx context.Context, paymentID uuid.UUID) error {
	pm, err := p.db.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return err
	}
	if pm.Status != "pending" {
		return nil
	}
	if pm.BillID == "" {
		return fmt.Errorf("payment %s has no bill_id yet", pm.ID)
	}
	return p.reconcile(ctx, pm)
}

// rewardReferrerIfFirstPayment atomically claims any pending referral reward
// for this user (i.e. on their first successful payment) and credits the
// referrer with the bonus days. Idempotent — ClaimReferralReward returns
// sql.ErrNoRows on subsequent calls.
func (p *Poller) rewardReferrerIfFirstPayment(ctx context.Context, userID uuid.UUID) {
	if p.extender == nil {
		return
	}
	ref, err := p.db.ClaimReferralReward(ctx, userID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("claim referral reward", "user_id", userID, "err", err)
		}
		return
	}
	if err := p.extender.ExtendUserSubscription(ctx, ref.ReferrerID, ref.BonusDays); err != nil {
		slog.Error("extend referrer", "referrer_id", ref.ReferrerID, "err", err)
	} else {
		slog.Info("referrer rewarded", "referrer_id", ref.ReferrerID, "referred_id", userID, "days", ref.BonusDays)
	}
}

func (p *Poller) reconcile(ctx context.Context, pm *db.Payment) error {
	t, err := p.platega.GetTransaction(ctx, pm.BillID)
	if err != nil {
		slog.Warn("platega get transaction", "payment_id", pm.ID, "err", err)
		return err
	}
	switch {
	case platega.IsConfirmed(t.Status):
		desc := fmt.Sprintf("Platega top-up %s", pm.BillID)
		if _, err := p.db.CreditBalance(ctx, pm.UserID, pm.AmountKopecks, "platega_topup", desc, nil); err != nil {
			slog.Error("platega credit balance", "payment_id", pm.ID, "err", err)
			return err
		}
		if err := p.db.UpdatePaymentStatus(ctx, pm.ID, "success", true); err != nil {
			slog.Error("platega mark paid", "payment_id", pm.ID, "err", err)
		}
		slog.Info("platega payment confirmed", "payment_id", pm.ID, "amount_kopecks", pm.AmountKopecks)
		p.rewardReferrerIfFirstPayment(ctx, pm.UserID)
	case platega.IsFailed(t.Status):
		_ = p.db.UpdatePaymentStatus(ctx, pm.ID, "fail", false)
		slog.Info("platega payment failed", "payment_id", pm.ID, "status", t.Status)
	}
	return nil
}
