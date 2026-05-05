package subscription

import (
	"context"
	"log"

	"github.com/robfig/cron/v3"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/provisioner"
)

// Scheduler handles background jobs for subscription lifecycle.
type Scheduler struct {
	db          *db.DB
	provisioner *provisioner.Service
	cron        *cron.Cron
}

func NewScheduler(database *db.DB, prov *provisioner.Service) *Scheduler {
	return &Scheduler{
		db:          database,
		provisioner: prov,
		cron:        cron.New(),
	}
}

func (s *Scheduler) Start() {
	// Every hour: expire subscriptions and deprovision users
	s.cron.AddFunc("@every 1h", s.expireSubscriptions)
	s.cron.Start()
	log.Println("subscription scheduler started")
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) expireSubscriptions() {
	ctx := context.Background()

	subs, err := s.db.ListExpiredActiveSubscriptions(ctx)
	if err != nil {
		log.Printf("expireSubscriptions: list: %v", err)
		return
	}

	for _, sub := range subs {
		if err := s.provisioner.Deprovision(ctx, sub.UserID); err != nil {
			log.Printf("expireSubscriptions: deprovision user %s: %v", sub.UserID, err)
			// Continue to next user; mark as inactive regardless
		}
		if err := s.db.ExpireSubscription(ctx, sub.ID); err != nil {
			log.Printf("expireSubscriptions: expire sub %s: %v", sub.ID, err)
		}
	}

	if len(subs) > 0 {
		log.Printf("expireSubscriptions: processed %d expired subscriptions", len(subs))
	}
}
