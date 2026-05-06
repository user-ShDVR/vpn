package provisioner

import (
	"context"
	"fmt"
	"log"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/panel"
)

type Service struct {
	db     *db.DB
	mu     sync.Mutex
	panels map[uuid.UUID]*panel.Client
}

func New(database *db.DB) *Service {
	return &Service{db: database, panels: make(map[uuid.UUID]*panel.Client)}
}

func (s *Service) panelFor(srv *db.Server) *panel.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pc, ok := s.panels[srv.ID]; ok {
		return pc
	}
	pc := panel.New(srv.PanelURL, srv.PanelUser, srv.PanelPass, srv.SubURL, srv.SubPath)
	s.panels[srv.ID] = pc
	return pc
}

// Provision creates VPN clients on top-N least-loaded entry servers via 3x-ui API.
// Best-effort: returns URIs from servers that succeeded. Errors only if all fail.
// N comes from plan.ServerCount (fallback 1 for legacy plans).
func (s *Service) Provision(ctx context.Context, user *db.User, sub *db.Subscription) ([]string, error) {
	plan, err := s.db.GetPlanByID(ctx, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	n := plan.ServerCount
	if n <= 0 {
		n = 1
	}
	servers, err := s.db.GetTopNLeastLoadedEntryServers(ctx, n)
	if err != nil || len(servers) == 0 {
		return nil, fmt.Errorf("no available entry servers: %w", err)
	}

	var (
		uris   []string
		errs   []string
	)
	for i := range servers {
		uri, err := s.provisionOnServer(ctx, user, plan, &servers[i])
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", servers[i].Name, err))
			continue
		}
		uris = append(uris, uri)
	}

	if len(uris) == 0 {
		return nil, fmt.Errorf("provisioning failed on all servers: %s", strings.Join(errs, "; "))
	}
	return uris, nil
}

func (s *Service) provisionOnServer(ctx context.Context, user *db.User, plan *db.Plan, server *db.Server) (string, error) {
	pc := s.panelFor(server)
	if err := pc.Login(ctx); err != nil {
		return "", fmt.Errorf("panel login: %w", err)
	}

	existing, err := s.db.GetServerClientByUserAndServer(ctx, user.ID, server.ID)
	if err == nil && existing != nil {
		uri, err := pc.GetSubURI(ctx, existing.SubID)
		if err != nil {
			return "", fmt.Errorf("get sub URI: %w", err)
		}
		return uri, nil
	}

	clientUUID := uuid.New()
	subID := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	// Per-server unique email so different server entries don't collide on the same 3x-ui inbound
	xrayEmail := fmt.Sprintf("%s-%s@vpn", user.ID.String()[:8], server.ID.String()[:4])

	var totalGB int64
	if plan.TrafficLimitGB != nil {
		totalGB = int64(*plan.TrafficLimitGB) * 1024 * 1024 * 1024
	}
	expiryMs := -int64(plan.DurationDays) * 24 * 60 * 60 * 1000

	xrayClient := panel.XrayClient{
		ID:         clientUUID.String(),
		Email:      xrayEmail,
		LimitIP:    plan.MaxDevices,
		TotalGB:    totalGB,
		ExpiryTime: expiryMs,
		Enable:     true,
		SubID:      subID,
	}

	if err := pc.AddClient(ctx, server.InboundID, xrayClient); err != nil {
		if strings.Contains(err.Error(), "Duplicate email") {
			_ = pc.DeleteClientByEmail(ctx, server.InboundID, xrayEmail)
			if err2 := pc.AddClient(ctx, server.InboundID, xrayClient); err2 != nil {
				return "", fmt.Errorf("add client to panel (retry): %w", err2)
			}
		} else {
			return "", fmt.Errorf("add client to panel: %w", err)
		}
	}

	if _, err := s.db.CreateServerClient(ctx, user.ID, server.ID, clientUUID, xrayEmail, subID); err != nil {
		_ = pc.DeleteClient(ctx, server.InboundID, clientUUID.String())
		return "", fmt.Errorf("save server client: %w", err)
	}
	_ = s.db.IncrementServerClientCount(ctx, server.ID)

	uri, err := pc.GetSubURI(ctx, subID)
	if err != nil {
		return "", fmt.Errorf("get sub URI: %w", err)
	}
	return uri, nil
}

// Deprovision removes the user's VPN clients from all servers.
func (s *Service) Deprovision(ctx context.Context, userID uuid.UUID) error {
	clients, err := s.db.GetServerClientsByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get server clients: %w", err)
	}

	servers, err := s.db.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	serverMap := make(map[uuid.UUID]*db.Server, len(servers))
	for i := range servers {
		serverMap[servers[i].ID] = &servers[i]
	}

	for _, sc := range clients {
		srv, ok := serverMap[sc.ServerID]
		if !ok {
			continue
		}
		pc := s.panelFor(srv)
		if err := pc.Login(ctx); err != nil {
			return fmt.Errorf("panel login for %s: %w", srv.Name, err)
		}
		if err := pc.DeleteClient(ctx, srv.InboundID, sc.ClientUUID.String()); err != nil {
			return fmt.Errorf("delete client from %s: %w", srv.Name, err)
		}
		_ = s.db.DecrementServerClientCount(ctx, sc.ServerID)
	}

	return s.db.DeleteServerClientsByUser(ctx, userID)
}

// ActivateFreePlanIfNone activates the cheapest non-Free plan for a user with
// no active subscription. Idempotent: returns existing sub if already active.
func (s *Service) ActivateFreePlanIfNone(ctx context.Context, user *db.User) (*db.Subscription, []string, error) {
	if existing, err := s.db.GetActiveSubscription(ctx, user.ID); err == nil && existing != nil {
		uris, _ := s.GetSubURIs(ctx, user.ID)
		if len(uris) > 0 {
			return existing, uris, nil
		}
		uris, err := s.Provision(ctx, user, existing)
		return existing, uris, err
	}

	plans, err := s.db.ListPlans(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list plans: %w", err)
	}
	var pick *db.Plan
	for i := range plans {
		if plans[i].Name == "Free" {
			continue
		}
		if pick == nil || plans[i].CostKopecks < pick.CostKopecks {
			pick = &plans[i]
		}
	}
	if pick == nil {
		return nil, nil, fmt.Errorf("no plan seeded")
	}

	expiresAt := time.Now().AddDate(0, 0, pick.DurationDays)
	sub, err := s.db.CreateSubscription(ctx, user.ID, pick.ID, expiresAt)
	if err != nil {
		return nil, nil, fmt.Errorf("create subscription: %w", err)
	}
	uris, err := s.Provision(ctx, user, sub)
	if err != nil {
		return sub, nil, err
	}
	return sub, uris, nil
}

// GetOnlineDevices returns the IPs currently using the user's VPN per server.
// Map key: server name. Empty list if no devices online.
func (s *Service) GetOnlineDevices(ctx context.Context, userID uuid.UUID) (map[string][]string, error) {
	rows, err := s.db.GetServerClientsWithNames(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	servers, err := s.db.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	serverMap := make(map[uuid.UUID]*db.Server, len(servers))
	for i := range servers {
		serverMap[servers[i].ID] = &servers[i]
	}

	out := make(map[string][]string, len(rows))
	for _, sc := range rows {
		srv, ok := serverMap[sc.ServerID]
		if !ok {
			continue
		}
		pc := s.panelFor(srv)
		if err := pc.Login(ctx); err != nil {
			log.Printf("[devices] login %s failed: %v", srv.Name, err)
			continue
		}
		ips, err := pc.GetClientIPs(ctx, sc.XrayEmail)
		if err != nil {
			log.Printf("[devices] %s ips for %s err: %v", srv.Name, sc.XrayEmail, err)
		}
		// Fallback: when xray access-log is off, clientIps returns empty.
		// Use onlines endpoint to detect at least connection state.
		if len(ips) == 0 {
			emails, oerr := pc.GetOnlineEmails(ctx)
			if oerr != nil {
				log.Printf("[devices] %s onlines err: %v", srv.Name, oerr)
			} else {
				for _, e := range emails {
					if e == sc.XrayEmail {
						ips = []string{"online"}
						break
					}
				}
			}
		}
		out[sc.ServerName] = ips
	}
	return out, nil
}

// ExtendUserSubscription bumps DB expiry by N days AND syncs the new absolute
// expiry to every xray client (so 3x-ui doesn't disconnect them when the
// original deadline hits).
func (s *Service) ExtendUserSubscription(ctx context.Context, userID uuid.UUID, days int) error {
	if err := s.db.ExtendSubscription(ctx, userID, days); err != nil {
		return fmt.Errorf("db extend: %w", err)
	}
	sub, err := s.db.GetActiveSubscription(ctx, userID)
	if err != nil {
		return nil // no active sub, nothing to sync
	}
	plan, err := s.db.GetPlanByID(ctx, sub.PlanID)
	if err != nil {
		return fmt.Errorf("get plan: %w", err)
	}
	clients, err := s.db.GetServerClientsByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get clients: %w", err)
	}
	if len(clients) == 0 {
		return nil
	}
	servers, err := s.db.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	srvMap := make(map[uuid.UUID]*db.Server, len(servers))
	for i := range servers {
		srvMap[servers[i].ID] = &servers[i]
	}
	expiryMs := sub.ExpiresAt.UnixMilli()
	var totalGB int64
	if plan.TrafficLimitGB != nil {
		totalGB = int64(*plan.TrafficLimitGB) * 1024 * 1024 * 1024
	}
	for _, sc := range clients {
		srv, ok := srvMap[sc.ServerID]
		if !ok {
			continue
		}
		pc := s.panelFor(srv)
		if err := pc.Login(ctx); err != nil {
			log.Printf("[extend] login %s failed: %v", srv.Name, err)
			continue
		}
		if err := pc.UpdateClient(ctx, srv.InboundID, sc.ClientUUID.String(), panel.XrayClient{
			ID:         sc.ClientUUID.String(),
			Email:      sc.XrayEmail,
			LimitIP:    plan.MaxDevices,
			TotalGB:    totalGB,
			ExpiryTime: expiryMs,
			Enable:     true,
			SubID:      sc.SubID,
		}); err != nil {
			log.Printf("[extend] update %s/%s failed: %v", srv.Name, sc.XrayEmail, err)
		}
	}
	return nil
}

// GetSubURIs returns all VLESS URIs for a user (one per provisioned server).
// Each URI's #remark is rewritten as "СвязьOK · <server_name>" so VPN clients
// show readable labels instead of the raw email/expiry suffix.
func (s *Service) GetSubURIs(ctx context.Context, userID uuid.UUID) ([]string, error) {
	clients, err := s.db.GetServerClientsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get server clients: %w", err)
	}
	if len(clients) == 0 {
		return nil, nil
	}

	servers, err := s.db.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	serverMap := make(map[uuid.UUID]*db.Server, len(servers))
	for i := range servers {
		serverMap[servers[i].ID] = &servers[i]
	}

	var uris []string
	for _, sc := range clients {
		srv, ok := serverMap[sc.ServerID]
		if !ok {
			continue
		}
		pc := s.panelFor(srv)
		if err := pc.Login(ctx); err != nil {
			continue
		}
		uri, err := pc.GetSubURI(ctx, sc.SubID)
		if err != nil || uri == "" {
			continue
		}
		uris = append(uris, relabelURIs(uri, "СвязьOK · "+srv.Name))
	}
	return uris, nil
}

// relabelURIs replaces the trailing #remark on each VLESS line with a custom
// label. Handles multi-line bodies (3x-ui returns one line per inbound).
func relabelURIs(body, label string) string {
	encoded := neturl.PathEscape(label)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx := strings.LastIndex(line, "#"); idx >= 0 {
			line = line[:idx+1] + encoded
		} else {
			line = line + "#" + encoded
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// GetTraffic fetches the user's traffic stats from 3x-ui.
func (s *Service) GetTraffic(ctx context.Context, userID uuid.UUID) (*panel.Traffic, error) {
	entryServer, err := s.db.GetEntryServer(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entry server: %w", err)
	}

	sc, err := s.db.GetServerClientByUserAndServer(ctx, userID, entryServer.ID)
	if err != nil {
		return nil, fmt.Errorf("get server client: %w", err)
	}

	pc := s.panelFor(entryServer)
	if err := pc.Login(ctx); err != nil {
		return nil, fmt.Errorf("panel login: %w", err)
	}

	traffic, err := pc.GetClientTraffic(ctx, sc.XrayEmail)
	if err != nil {
		return nil, fmt.Errorf("get client traffic: %w", err)
	}
	if traffic == nil {
		return &panel.Traffic{}, nil
	}
	return traffic, nil
}

// MigrateClients moves all clients from one server to another.
func (s *Service) MigrateClients(ctx context.Context, fromServerID, toServerID uuid.UUID) (migrated, failed int, errors []string) {
	fromServer, err := s.db.GetServerByID(ctx, fromServerID)
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("from server not found: %v", err)}
	}
	toServer, err := s.db.GetServerByID(ctx, toServerID)
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("to server not found: %v", err)}
	}

	fromPanel := s.panelFor(fromServer)
	toPanel := s.panelFor(toServer)
	if err := fromPanel.Login(ctx); err != nil {
		return 0, 0, []string{fmt.Sprintf("from panel login: %v", err)}
	}
	if err := toPanel.Login(ctx); err != nil {
		return 0, 0, []string{fmt.Sprintf("to panel login: %v", err)}
	}

	clients, err := s.db.GetServerClientsByServer(ctx, fromServerID)
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("get clients: %v", err)}
	}

	for _, sc := range clients {
		user, err := s.db.GetUserByID(ctx, sc.UserID)
		if err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("user %s not found", sc.UserID))
			continue
		}

		sub, err := s.db.GetActiveSubscription(ctx, sc.UserID)
		if err != nil {
			// No active sub, just delete from old server
			_ = fromPanel.DeleteClient(ctx, fromServer.InboundID, sc.ClientUUID.String())
			_ = s.db.DecrementServerClientCount(ctx, fromServerID)
			migrated++
			continue
		}

		plan, err := s.db.GetPlanByID(ctx, sub.PlanID)
		if err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("plan for user %s: %v", user.Email, err))
			continue
		}

		// Create new client on target server
		newClientUUID := uuid.New()
		newSubID := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]

		var totalGB int64
		if plan.TrafficLimitGB != nil {
			totalGB = int64(*plan.TrafficLimitGB) * 1024 * 1024 * 1024
		}
		expiryMs := -int64(plan.DurationDays) * 24 * 60 * 60 * 1000

		xrayClient := panel.XrayClient{
			ID:         newClientUUID.String(),
			Flow:       "",
			Email:      sc.XrayEmail,
			LimitIP:    plan.MaxDevices,
			TotalGB:    totalGB,
			ExpiryTime: expiryMs,
			Enable:     true,
			SubID:      newSubID,
		}

		// Delete old client first to free email
		_ = fromPanel.DeleteClient(ctx, fromServer.InboundID, sc.ClientUUID.String())

		if err := toPanel.AddClient(ctx, toServer.InboundID, xrayClient); err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("add client %s to target: %v", user.Email, err))
			continue
		}

		// Update DB record
		if err := s.db.UpdateServerClient(ctx, sc.ID, toServerID, newClientUUID, newSubID); err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("update db for %s: %v", user.Email, err))
			continue
		}

		_ = s.db.DecrementServerClientCount(ctx, fromServerID)
		_ = s.db.IncrementServerClientCount(ctx, toServerID)
		migrated++
	}
	return
}

// GetSubURL returns the subscription URL that can be imported into v2rayNG/Streisand.
func (s *Service) GetSubURL(ctx context.Context, userID uuid.UUID) (string, error) {
	entryServer, err := s.db.GetEntryServer(ctx)
	if err != nil {
		return "", fmt.Errorf("get entry server: %w", err)
	}

	sc, err := s.db.GetServerClientByUserAndServer(ctx, userID, entryServer.ID)
	if err != nil {
		return "", nil
	}

	pc := s.panelFor(entryServer)
	return pc.GetSubURL(sc.SubID), nil
}
