package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/auth"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/payment/pally"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	db            *db.DB
	provisioner   *provisioner.Service
	jwtSecret     string
	publicBaseURL string
	pally         *pally.Client
}

func NewHandler(database *db.DB, prov *provisioner.Service, jwtSecret, publicBaseURL string, pallyCli *pally.Client) *Handler {
	return &Handler{
		db: database, provisioner: prov,
		jwtSecret: jwtSecret, publicBaseURL: publicBaseURL,
		pally: pallyCli,
	}
}

func (h *Handler) Register(app *fiber.App) {
	// Public subscription endpoint for ready-made clients (Happ, V2RayTun, Hiddify).
	// Rate-limit to slow brute-force enumeration of subscription_token values.
	subLimiter := limiter.New(limiter.Config{
		Max:        30,
		Expiration: 1 * time.Minute,
		LimitReached: func(c *fiber.Ctx) error {
			return fiber.NewError(fiber.StatusTooManyRequests, "too many requests")
		},
	})
	app.Get("/sub/:token", subLimiter, h.publicGetSubscription)

	// Pally webhooks (signature-verified, no JWT). Light rate cap; bad signatures
	// already 401, so this protects only against abuse-floods.
	pallyLimiter := limiter.New(limiter.Config{
		Max:        60,
		Expiration: 1 * time.Minute,
	})
	app.Post("/webhook/pally/result", pallyLimiter, h.pallyResult)
	app.Post("/webhook/pally/success", h.pallySuccess)
	app.Post("/webhook/pally/fail", h.pallyFail)

	v1 := app.Group("/api/v1")

	// Auth (rate limited: 10 req/min per IP)
	authLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Minute,
		LimitReached: func(c *fiber.Ctx) error {
			return fiber.NewError(fiber.StatusTooManyRequests, "too many requests, try again later")
		},
	})
	v1.Post("/auth/register", authLimiter, h.authRegister)
	v1.Post("/auth/login", authLimiter, h.authLogin)
	v1.Get("/auth/me", h.jwtMiddleware, h.authMe)
	v1.Post("/auth/logout", h.jwtMiddleware, h.authLogout)
	v1.Put("/auth/password", h.jwtMiddleware, h.changePassword)

	// App version (public)
	v1.Get("/app/version", h.getAppVersion)

	// Plans
	v1.Get("/plans", h.listPlans)

	// Subscription (auth required)
	v1.Get("/subscription", h.jwtMiddleware, h.getSubscription)
	v1.Post("/subscription/activate", h.jwtMiddleware, h.activateSubscription)

	// Auto-provision: creates free plan + generates config if needed
	v1.Post("/provision", h.jwtMiddleware, h.autoProvision)

	// Subscription URL helpers (legacy /config, /subscription-link removed; use /sub/:token).
	v1.Get("/subscription/url", h.jwtMiddleware, h.getSubscriptionURL)
	v1.Post("/subscription/rotate", h.jwtMiddleware, h.rotateSubscriptionToken)

	// Balance
	v1.Get("/balance", h.jwtMiddleware, h.getBalance)
	v1.Post("/balance/topup", h.jwtMiddleware, h.topupBalance)
	v1.Post("/subscription/buy", h.jwtMiddleware, h.buyPlan)

	// Traffic
	v1.Get("/traffic", h.jwtMiddleware, h.getTraffic)

	// Referrals
	v1.Get("/referral", h.jwtMiddleware, h.getReferralCode)
	v1.Post("/referral/apply", h.jwtMiddleware, h.applyReferral)

	// Admin routes
	admin := v1.Group("/admin", h.jwtMiddleware, h.adminMiddleware)
	admin.Get("/users", h.adminListUsers)
	admin.Post("/plans", h.adminCreatePlan)
	admin.Post("/subscription/grant", h.adminGrantSubscription)
	admin.Post("/servers", h.adminCreateServer)
	admin.Get("/servers", h.adminListServers)
	admin.Get("/stats", h.adminStats)
	admin.Put("/servers/:id", h.adminUpdateServer)
	admin.Post("/servers/migrate", h.adminMigrateServer)
	admin.Post("/balance/topup", h.adminBalanceTopup)
	admin.Get("/promo", h.adminListPromo)
	admin.Post("/promo", h.adminCreatePromo)
}

// --- Auth ---

type registerRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	ReferralCode string `json:"referral_code,omitempty"`
}

func (h *Handler) authRegister(c *fiber.Ctx) error {
	var req registerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.Email == "" || len(req.Password) < 8 {
		return fiber.NewError(fiber.StatusBadRequest, "email required, password min 8 chars")
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid email format")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	user, err := h.db.CreateUser(c.Context(), req.Email, string(hash))
	if err != nil {
		return fiber.NewError(fiber.StatusConflict, "email already registered")
	}

	// Auto-apply referral code if provided at registration
	if req.ReferralCode != "" {
		referrer, refErr := h.db.GetUserByReferralCode(c.Context(), req.ReferralCode)
		if refErr == nil && referrer.ID != user.ID {
			if _, refErr := h.db.CreateReferral(c.Context(), referrer.ID, user.ID, referralBonusDays); refErr == nil {
				_ = h.db.ExtendSubscription(c.Context(), referrer.ID, referralBonusDays)
				_ = h.db.ExtendSubscription(c.Context(), user.ID, referralBonusDays)
			}
		}
	}

	token, err := auth.GenerateToken(user.ID, user.Role, h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	return c.JSON(fiber.Map{"token": token, "user": userResponse(user)})
}

func (h *Handler) authLogin(c *fiber.Ctx) error {
	var req registerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}

	user, err := h.db.GetUserByEmail(c.Context(), req.Email)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}

	token, err := auth.GenerateToken(user.ID, user.Role, h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	return c.JSON(fiber.Map{"token": token, "user": userResponse(user)})
}

func (h *Handler) authLogout(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"message": "logged out"})
}

func (h *Handler) authMe(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	return c.JSON(userResponse(user))
}

// --- Plans ---

func (h *Handler) getAppVersion(c *fiber.Ctx) error {
	versionCode := 1
	if v := os.Getenv("APP_VERSION_CODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			versionCode = n
		}
	}
	versionName := os.Getenv("APP_VERSION_NAME")
	if versionName == "" {
		versionName = "1.0.0"
	}
	apkURL := os.Getenv("APP_APK_URL")
	forceUpdate := os.Getenv("APP_FORCE_UPDATE") == "true"
	changelog := os.Getenv("APP_CHANGELOG")

	return c.JSON(fiber.Map{
		"version_code": versionCode,
		"version_name": versionName,
		"apk_url":      apkURL,
		"force_update": forceUpdate,
		"changelog":    changelog,
	})
}

func (h *Handler) listPlans(c *fiber.Ctx) error {
	plans, err := h.db.ListPlans(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(plans)
}

// --- Subscription ---

func (h *Handler) getSubscription(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	sub, err := h.db.GetActiveSubscription(c.Context(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(fiber.Map{"subscription": nil, "plan": nil})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	plan, _ := h.db.GetPlanByID(c.Context(), sub.PlanID)
	return c.JSON(fiber.Map{"subscription": sub, "plan": plan})
}

type activateRequest struct {
	PlanID string `json:"plan_id"`
	Code   string `json:"code"` // promo code (future use)
}

func (h *Handler) activateSubscription(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	var req activateRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}

	planID, err := uuid.Parse(req.PlanID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid plan_id")
	}

	plan, err := h.db.GetPlanByID(c.Context(), planID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "plan not found")
	}

	// Deactivate existing subscription and deprovision old client
	if _, err := h.db.GetActiveSubscription(c.Context(), userID); err == nil {
		_ = h.provisioner.Deprovision(c.Context(), userID)
		_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
	}

	expiresAt := time.Now().AddDate(0, 0, plan.DurationDays)
	sub, err := h.db.CreateSubscription(c.Context(), userID, planID, expiresAt)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	uris, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed for user: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}

	return c.JSON(fiber.Map{
		"subscription": sub,
		"vless_uris":   uris,
	})
}

// --- Auto Provision (free plan for new users) ---

func (h *Handler) autoProvision(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	// Check if user already has active subscription
	existingSub, err := h.db.GetActiveSubscription(c.Context(), userID)
	if err == nil && existingSub != nil {
		uris, err := h.provisioner.GetSubURIs(c.Context(), userID)
		if err != nil {
			log.Printf("get config failed: %v", err)
			return fiber.NewError(fiber.StatusInternalServerError, "failed to get config")
		}
		if len(uris) == 0 {
			user, _ := h.db.GetUserByID(c.Context(), userID)
			uris, err = h.provisioner.Provision(c.Context(), user, existingSub)
			if err != nil {
				log.Printf("provision failed: %v", err)
				return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
			}
		}
		return c.JSON(fiber.Map{
			"status":       "ok",
			"subscription": existingSub,
			"vless_uris":   uris,
		})
	}

	// No subscription - find Free plan and activate
	plans, err := h.db.ListPlans(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}

	var freePlan *db.Plan
	for i := range plans {
		if plans[i].Name == "Free" {
			freePlan = &plans[i]
			break
		}
	}
	if freePlan == nil {
		return fiber.NewError(fiber.StatusInternalServerError, "free plan not found")
	}

	// Create subscription
	expiresAt := time.Now().Add(time.Duration(freePlan.DurationDays) * 24 * time.Hour)
	sub, err := h.db.CreateSubscription(c.Context(), userID, freePlan.ID, expiresAt)
	if err != nil {
		log.Printf("create subscription failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create subscription")
	}

	// Provision VPN config
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	uris, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}

	return c.JSON(fiber.Map{
		"status":       "provisioned",
		"subscription": sub,
		"vless_uris":   uris,
	})
}

// --- Config ---

// Legacy endpoints /config and /subscription-link removed. Clients should use:
//   - GET  /api/v1/subscription/url       — JWT, returns full URL + QR PNG
//   - GET  /sub/:token                    — public, base64 VLESS list (the URL)

// --- Referrals ---

func (h *Handler) getReferralCode(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	// Generate referral code if not exists
	if user.ReferralCode == nil {
		code := uuid.New().String()[:8]
		if err := h.db.SetReferralCode(c.Context(), userID, code); err != nil {
			return fiber.ErrInternalServerError
		}
		return c.JSON(fiber.Map{"referral_code": code, "referral_count": 0})
	}

	count, _ := h.db.CountReferrals(c.Context(), userID)
	return c.JSON(fiber.Map{
		"referral_code":  *user.ReferralCode,
		"referral_count": count,
	})
}

type applyReferralRequest struct {
	Code string `json:"code"`
}

const referralBonusDays = 5

func (h *Handler) applyReferral(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	var req applyReferralRequest
	if err := c.BodyParser(&req); err != nil || req.Code == "" {
		return fiber.NewError(fiber.StatusBadRequest, "referral code required")
	}

	// Check if user already used a referral
	existing, _ := h.db.GetReferralByReferred(c.Context(), userID)
	if existing != nil {
		return fiber.NewError(fiber.StatusConflict, "referral already applied")
	}

	// Find referrer
	referrer, err := h.db.GetUserByReferralCode(c.Context(), req.Code)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "invalid referral code")
	}

	// Can't refer yourself
	if referrer.ID == userID {
		return fiber.NewError(fiber.StatusBadRequest, "cannot use own referral code")
	}

	// Create referral record
	if _, err := h.db.CreateReferral(c.Context(), referrer.ID, userID, referralBonusDays); err != nil {
		return fiber.NewError(fiber.StatusConflict, "referral already applied")
	}

	// Extend both subscriptions by 5 days
	_ = h.db.ExtendSubscription(c.Context(), referrer.ID, referralBonusDays)
	_ = h.db.ExtendSubscription(c.Context(), userID, referralBonusDays)

	return c.JSON(fiber.Map{
		"message":    fmt.Sprintf("Both you and the referrer received %d bonus days", referralBonusDays),
		"bonus_days": referralBonusDays,
	})
}

// --- Traffic ---

func (h *Handler) getTraffic(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	_, err := h.db.GetActiveSubscription(c.Context(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return fiber.NewError(fiber.StatusForbidden, "no active subscription")
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}

	traffic, err := h.provisioner.GetTraffic(c.Context(), userID)
	if err != nil {
		log.Printf("get traffic failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get traffic")
	}

	return c.JSON(fiber.Map{
		"up":    traffic.Up,
		"down":  traffic.Down,
		"total": traffic.Total,
	})
}

// --- Admin ---

func (h *Handler) adminListUsers(c *fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	users, err := h.db.ListUsers(c.Context(), limit, offset)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	result := make([]fiber.Map, len(users))
	for i, u := range users {
		result[i] = userResponse(&u)
	}
	return c.JSON(fiber.Map{
		"users":  result,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *Handler) adminListServers(c *fiber.Ctx) error {
	servers, err := h.db.ListServers(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	// Mask credentials
	result := make([]fiber.Map, len(servers))
	for i, s := range servers {
		result[i] = fiber.Map{
			"id":           s.ID,
			"name":         s.Name,
			"type":         s.Type,
			"host":         s.Host,
			"port":         s.Port,
			"is_active":    s.IsActive,
			"max_clients":  s.MaxClients,
			"client_count": s.ClientCount,
		}
	}
	return c.JSON(result)
}

func (h *Handler) adminStats(c *fiber.Ctx) error {
	users, _ := h.db.CountUsers(c.Context())
	activeSubs, _ := h.db.CountActiveSubscriptions(c.Context())
	servers, _ := h.db.CountServers(c.Context())
	return c.JSON(fiber.Map{
		"users":                users,
		"active_subscriptions": activeSubs,
		"active_servers":       servers,
	})
}

type createPlanRequest struct {
	Name           string `json:"name"`
	DurationDays   int    `json:"duration_days"`
	TrafficLimitGB *int   `json:"traffic_limit_gb"`
	MaxDevices     int    `json:"max_devices"`
	CostKopecks    int64  `json:"cost_kopecks"`
	Description    string `json:"description"`
}

func (h *Handler) adminCreatePlan(c *fiber.Ctx) error {
	var req createPlanRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	plan, err := h.db.CreatePlan(c.Context(), req.Name, req.DurationDays, req.TrafficLimitGB, req.MaxDevices, req.CostKopecks, req.Description)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(plan)
}

type grantSubscriptionRequest struct {
	UserID string `json:"user_id"`
	PlanID string `json:"plan_id"`
}

func (h *Handler) adminGrantSubscription(c *fiber.Ctx) error {
	var req grantSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid user_id")
	}
	planID, err := uuid.Parse(req.PlanID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid plan_id")
	}

	plan, err := h.db.GetPlanByID(c.Context(), planID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "plan not found")
	}

	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	expiresAt := time.Now().AddDate(0, 0, plan.DurationDays)
	sub, err := h.db.CreateSubscription(c.Context(), userID, planID, expiresAt)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	uris, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"subscription": sub,
		"vless_uris":   uris,
	})
}

type createServerRequest struct {
	Name      string `json:"name"`
	PanelURL  string `json:"panel_url"`
	PanelUser string `json:"panel_user"`
	PanelPass string `json:"panel_pass"`
	InboundID int    `json:"inbound_id"`
	Type      string `json:"type"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	SubURL    string `json:"sub_url"`   // 3x-ui subscription service URL
	SubPath   string `json:"sub_path"`  // subscription path (e.g. /mysecret/sub/)
}

func (h *Handler) adminCreateServer(c *fiber.Ctx) error {
	var req createServerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.Type != "entry" && req.Type != "exit" {
		return fiber.NewError(fiber.StatusBadRequest, "type must be 'entry' or 'exit'")
	}
	if req.SubPath == "" {
		req.SubPath = "/sub/"
	}
	server, err := h.db.CreateServer(c.Context(), db.Server{
		Name:      req.Name,
		PanelURL:  req.PanelURL,
		PanelUser: req.PanelUser,
		PanelPass: req.PanelPass,
		InboundID: req.InboundID,
		Type:      req.Type,
		Host:      req.Host,
		Port:      req.Port,
		SubURL:    req.SubURL,
		SubPath:   req.SubPath,
	})
	if err != nil {
		return fiber.ErrInternalServerError
	}
	// Mask credentials in response
	server.PanelPass = "***"
	return c.Status(fiber.StatusCreated).JSON(server)
}

// --- Update Server ---

type updateServerRequest struct {
	IsActive   *bool `json:"is_active"`
	MaxClients *int  `json:"max_clients"`
}

func (h *Handler) adminUpdateServer(c *fiber.Ctx) error {
	serverID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid server id")
	}
	var req updateServerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if err := h.db.UpdateServer(c.Context(), serverID, req.IsActive, req.MaxClients); err != nil {
		return fiber.ErrInternalServerError
	}
	srv, _ := h.db.GetServerByID(c.Context(), serverID)
	if srv != nil {
		srv.PanelPass = "***"
	}
	return c.JSON(srv)
}

// --- Migrate Server ---

type migrateServerRequest struct {
	FromServerID string `json:"from_server_id"`
	ToServerID   string `json:"to_server_id"`
}

func (h *Handler) adminMigrateServer(c *fiber.Ctx) error {
	var req migrateServerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	fromID, err := uuid.Parse(req.FromServerID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid from_server_id")
	}
	toID, err := uuid.Parse(req.ToServerID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid to_server_id")
	}
	migrated, failed, errors := h.provisioner.MigrateClients(c.Context(), fromID, toID)
	return c.JSON(fiber.Map{
		"migrated": migrated,
		"failed":   failed,
		"errors":   errors,
	})
}

// --- Change Password ---

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changePassword(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	var req changePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if len(req.NewPassword) < 8 {
		return fiber.NewError(fiber.StatusBadRequest, "new password min 8 chars")
	}

	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "current password is incorrect")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	if err := h.db.UpdatePassword(c.Context(), userID, string(hash)); err != nil {
		return fiber.ErrInternalServerError
	}

	return c.JSON(fiber.Map{"message": "password changed"})
}

// --- Public subscription (no auth) ---

func (h *Handler) publicGetSubscription(c *fiber.Ctx) error {
	token := c.Params("token")
	if len(token) < 16 {
		return fiber.ErrNotFound
	}

	user, err := h.db.GetUserBySubscriptionToken(c.Context(), token)
	if err != nil {
		return fiber.ErrNotFound
	}

	sub, subErr := h.db.GetActiveSubscription(c.Context(), user.ID)

	var (
		uris      []string
		expireUnix int64
		totalBytes int64
	)
	if subErr == nil && sub != nil {
		expireUnix = sub.ExpiresAt.Unix()
		uris, _ = h.provisioner.GetSubURIs(c.Context(), user.ID)
		if plan, err := h.db.GetPlanByID(c.Context(), sub.PlanID); err == nil && plan.TrafficLimitGB != nil {
			totalBytes = int64(*plan.TrafficLimitGB) * 1024 * 1024 * 1024
		}
	}

	body := base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n")))

	titleB64 := base64.StdEncoding.EncodeToString([]byte("СвязьOK"))
	c.Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=0; total=%d; expire=%d", totalBytes, expireUnix))
	c.Set("Profile-Update-Interval", "24")
	c.Set("Profile-Title", "base64:"+titleB64)
	c.Set("Content-Disposition", "attachment; filename=svyaz-ok.txt")
	c.Set("Content-Type", "text/plain; charset=utf-8")
	return c.SendString(body)
}

// --- Subscription URL + rotate (JWT) ---

func (h *Handler) getSubscriptionURL(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	subURL := h.publicBaseURL + "/sub/" + user.SubscriptionToken
	png, err := qrcode.Encode(subURL, qrcode.Medium, 256)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"url":           subURL,
		"qr_png_base64": base64.StdEncoding.EncodeToString(png),
	})
}

func (h *Handler) rotateSubscriptionToken(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	newToken, err := h.db.RotateSubscriptionToken(c.Context(), userID)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"subscription_token": newToken,
		"url":                h.publicBaseURL + "/sub/" + newToken,
	})
}

// --- Balance ---

func (h *Handler) getBalance(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	txs, err := h.db.GetTransactionsByUser(c.Context(), userID, limit)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"balance_kopecks": user.BalanceKopecks,
		"transactions":    txs,
	})
}

type buyPlanRequest struct {
	PlanID    string `json:"plan_id"`
	PromoCode string `json:"promo_code"`
}

// applyPromo evaluates a promo code against a plan and returns the effective
// cost in kopecks plus extra days to add to expiry. Errors map to user-friendly
// messages handled by callers.
func (h *Handler) applyPromo(ctx context.Context, userID uuid.UUID, planCost int64, code string, subID *uuid.UUID) (effectiveCost int64, bonusDays int, err error) {
	if code == "" {
		return planCost, 0, nil
	}
	p, err := h.db.GetPromoCode(ctx, code)
	if err != nil {
		return 0, 0, db.ErrPromoNotFound
	}
	var discount int64
	switch p.Kind {
	case "discount_percent":
		discount = planCost * p.Value / 100
		if discount > planCost {
			discount = planCost
		}
	case "discount_kopecks":
		discount = p.Value
		if discount > planCost {
			discount = planCost
		}
	case "bonus_days":
		bonusDays = int(p.Value)
	}
	if _, err := h.db.RedeemPromo(ctx, userID, code, subID, discount, bonusDays); err != nil {
		return 0, 0, err
	}
	return planCost - discount, bonusDays, nil
}

func (h *Handler) buyPlan(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	var req buyPlanRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	planID, err := uuid.Parse(req.PlanID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid plan_id")
	}
	plan, err := h.db.GetPlanByID(c.Context(), planID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "plan not found")
	}

	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	// Tear down existing sub before charging — refund happens implicitly via fresh expiry calc
	if _, err := h.db.GetActiveSubscription(c.Context(), userID); err == nil {
		_ = h.provisioner.Deprovision(c.Context(), userID)
		_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
	}

	expiresAt := time.Now().AddDate(0, 0, plan.DurationDays)
	sub, err := h.db.CreateSubscription(c.Context(), userID, planID, expiresAt)
	if err != nil {
		return fiber.ErrInternalServerError
	}

	effectiveCost, bonusDays, err := h.applyPromo(c.Context(), userID, plan.CostKopecks, req.PromoCode, &sub.ID)
	if err != nil {
		_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
		return fiber.NewError(fiber.StatusBadRequest, "promo code invalid: "+err.Error())
	}
	if bonusDays > 0 {
		_ = h.db.ExtendSubscription(c.Context(), userID, bonusDays)
	}

	if effectiveCost > 0 {
		desc := fmt.Sprintf("Plan: %s (%d days)", plan.Name, plan.DurationDays)
		if _, err := h.db.DebitBalance(c.Context(), userID, effectiveCost, "plan_purchase", desc, &sub.ID); err != nil {
			_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
			if errors.Is(err, db.ErrInsufficientBalance) {
				return fiber.NewError(fiber.StatusPaymentRequired, "insufficient balance")
			}
			return fiber.ErrInternalServerError
		}
	}

	uris, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}

	return c.JSON(fiber.Map{
		"subscription": sub,
		"vless_uris":   uris,
	})
}

// --- User-initiated top-up via Pally ---

type topupRequest struct {
	AmountKopecks int64 `json:"amount_kopecks"`
}

func (h *Handler) topupBalance(c *fiber.Ctx) error {
	if h.pally == nil || !h.pally.Configured() {
		return fiber.NewError(fiber.StatusServiceUnavailable, "payment provider not configured")
	}
	userID := c.Locals("userID").(uuid.UUID)
	var req topupRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.AmountKopecks < 10000 { // min 100 RUB
		return fiber.NewError(fiber.StatusBadRequest, "minimum amount is 100 RUB")
	}
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	payment, err := h.db.CreatePayment(c.Context(), db.Payment{
		UserID:        userID,
		Provider:      "pally",
		BillID:        "",
		AmountKopecks: req.AmountKopecks,
		Currency:      "RUB",
		Status:        "pending",
	})
	if err != nil {
		return fiber.ErrInternalServerError
	}

	bill, err := h.pally.CreateBill(c.Context(), pally.CreateBillRequest{
		Amount:              float64(req.AmountKopecks) / 100,
		OrderID:             payment.ID.String(),
		Description:         fmt.Sprintf("Top-up balance for %s", user.Email),
		Custom:              payment.ID.String(),
		Name:                "СвязьOK",
		PayerPaysCommission: true,
	})
	if err != nil {
		log.Printf("pally CreateBill: %v", err)
		_ = h.db.UpdatePaymentStatus(c.Context(), payment.ID, "fail", false)
		return fiber.NewError(fiber.StatusBadGateway, "payment gateway error")
	}

	_, err = h.db.GetPaymentByID(c.Context(), payment.ID)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := h.db.ExecContext(c.Context(),
		`UPDATE payments SET bill_id = $1, link_url = $2, pay_url = $3 WHERE id = $4`,
		bill.BillID, bill.LinkURL, bill.LinkPageURL, payment.ID,
	); err != nil {
		return fiber.ErrInternalServerError
	}

	return c.JSON(fiber.Map{
		"payment_id":    payment.ID,
		"pay_url":       bill.LinkPageURL,
		"amount_rubles": float64(req.AmountKopecks) / 100,
	})
}

// --- Pally webhook handlers ---

func (h *Handler) pallyResult(c *fiber.Ctx) error {
	if h.pally == nil {
		return fiber.ErrServiceUnavailable
	}
	status := c.FormValue("Status")
	invID := c.FormValue("InvId")
	outSum := c.FormValue("OutSum")
	trsID := c.FormValue("TrsId")
	signature := c.FormValue("SignatureValue")

	if !h.pally.VerifyPostbackSignature(outSum, invID, signature) {
		log.Printf("pally postback: signature mismatch invId=%s trsId=%s", invID, trsID)
		return fiber.ErrUnauthorized
	}

	paymentID, err := uuid.Parse(invID)
	if err != nil {
		log.Printf("pally postback: invalid InvId %q", invID)
		return fiber.ErrBadRequest
	}
	payment, err := h.db.GetPaymentByID(c.Context(), paymentID)
	if err != nil {
		log.Printf("pally postback: payment %s not found", paymentID)
		return fiber.ErrNotFound
	}
	if payment.Status == "success" {
		// already credited (idempotent retry)
		return c.SendString("OK")
	}

	if status == "SUCCESS" {
		desc := fmt.Sprintf("Pally top-up %s", trsID)
		if _, err := h.db.CreditBalance(c.Context(), payment.UserID, payment.AmountKopecks, "pally_topup", desc, nil); err != nil {
			log.Printf("pally postback: credit failed: %v", err)
			return fiber.ErrInternalServerError
		}
		if err := h.db.UpdatePaymentStatus(c.Context(), payment.ID, "success", true); err != nil {
			log.Printf("pally postback: status update: %v", err)
		}
	} else {
		_ = h.db.UpdatePaymentStatus(c.Context(), payment.ID, "fail", false)
	}

	return c.SendString("OK")
}

// pallySuccess / pallyFail are the browser-facing redirect endpoints.
// Pally posts the user's browser to these after payment; we just send them
// back to the cabinet so they can see status.
func (h *Handler) pallySuccess(c *fiber.Ctx) error {
	return c.Redirect("/balance?paid=1", fiber.StatusFound)
}

func (h *Handler) pallyFail(c *fiber.Ctx) error {
	return c.Redirect("/balance?paid=0", fiber.StatusFound)
}

// --- Admin balance top-up ---

type adminTopupRequest struct {
	UserID        string `json:"user_id"`
	AmountKopecks int64  `json:"amount_kopecks"`
	Description   string `json:"description"`
}

func (h *Handler) adminBalanceTopup(c *fiber.Ctx) error {
	var req adminTopupRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid user_id")
	}
	if req.AmountKopecks <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "amount must be positive")
	}
	tx, err := h.db.CreditBalance(c.Context(), userID, req.AmountKopecks, "admin_topup", req.Description, nil)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(tx)
}

// --- Admin promo ---

type createPromoRequest struct {
	Code      string  `json:"code"`
	Kind      string  `json:"kind"` // discount_percent|discount_kopecks|bonus_days
	Value     int64   `json:"value"`
	MaxUses   *int    `json:"max_uses"`
	ExpiresAt *string `json:"expires_at"` // RFC3339
	IsActive  bool    `json:"is_active"`
}

func (h *Handler) adminCreatePromo(c *fiber.Ctx) error {
	var req createPromoRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	switch req.Kind {
	case "discount_percent", "discount_kopecks", "bonus_days":
	default:
		return fiber.NewError(fiber.StatusBadRequest, "invalid kind")
	}
	p := db.PromoCode{
		Code: strings.ToUpper(strings.TrimSpace(req.Code)),
		Kind: req.Kind, Value: req.Value, MaxUses: req.MaxUses, IsActive: req.IsActive,
	}
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "expires_at must be RFC3339")
		}
		p.ExpiresAt = &t
	}
	if err := h.db.CreatePromoCode(c.Context(), p); err != nil {
		return fiber.NewError(fiber.StatusConflict, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(p)
}

func (h *Handler) adminListPromo(c *fiber.Ctx) error {
	codes, err := h.db.ListPromoCodes(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(codes)
}

// --- Middleware ---

func (h *Handler) jwtMiddleware(c *fiber.Ctx) error {
	tokenStr := ""
	if v := c.Cookies("auth_token"); v != "" {
		tokenStr = v
	}
	if tokenStr == "" {
		hdr := c.Get("Authorization")
		if len(hdr) > 7 && hdr[:7] == "Bearer " {
			tokenStr = hdr[7:]
		}
	}
	if tokenStr == "" {
		// Legacy query-param fallback for old subscription links
		tokenStr = c.Query("token")
	}
	if tokenStr == "" {
		return fiber.ErrUnauthorized
	}

	claims, err := auth.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
	}

	c.Locals("userID", claims.UserID)
	c.Locals("role", claims.Role)
	return c.Next()
}

func (h *Handler) adminMiddleware(c *fiber.Ctx) error {
	role, _ := c.Locals("role").(string)
	if role != "admin" {
		return fiber.ErrForbidden
	}
	return c.Next()
}

// --- Helpers ---

func userResponse(u *db.User) fiber.Map {
	m := fiber.Map{
		"id":         u.ID,
		"email":      u.Email,
		"role":       u.Role,
		"created_at": u.CreatedAt,
	}
	if u.ReferralCode != nil {
		m["referral_code"] = *u.ReferralCode
	}
	return m
}
