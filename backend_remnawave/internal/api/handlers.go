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
	"github.com/shdvr/vpn-backend/internal/payment"
	"github.com/shdvr/vpn-backend/internal/payment/platega"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	db                  *db.DB
	provisioner         *provisioner.Service
	jwtSecret           string
	publicBaseURL       string
	platega             *platega.Client
	poller              *payment.Poller
	plategaReturn       string
	plategaFail         string
	plategaMerchantID   string
	plategaSecret       string
}

type Config struct {
	DB                *db.DB
	Provisioner       *provisioner.Service
	JWTSecret         string
	PublicBaseURL     string
	Platega           *platega.Client
	Poller            *payment.Poller
	PlategaReturn     string
	PlategaFail       string
	PlategaMerchantID string
	PlategaSecret     string
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		db:                cfg.DB,
		provisioner:       cfg.Provisioner,
		jwtSecret:         cfg.JWTSecret,
		publicBaseURL:     cfg.PublicBaseURL,
		platega:           cfg.Platega,
		poller:            cfg.Poller,
		plategaReturn:     cfg.PlategaReturn,
		plategaFail:       cfg.PlategaFail,
		plategaMerchantID: cfg.PlategaMerchantID,
		plategaSecret:     cfg.PlategaSecret,
	}
}

func (h *Handler) Register(app *fiber.App) {
	// Platega server-to-server callback. Public route — auth is via header equality
	// against our own X-MerchantId / X-Secret env config. Light rate cap.
	webhookLimiter := limiter.New(limiter.Config{
		Max:        60,
		Expiration: 1 * time.Minute,
	})
	app.Post("/webhook/platega", webhookLimiter, h.plategaCallback)

	v1 := app.Group("/api/v1")

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

	v1.Get("/app/version", h.getAppVersion)

	v1.Get("/plans", h.listPlans)

	v1.Get("/subscription", h.jwtMiddleware, h.getSubscription)
	v1.Post("/subscription/activate", h.jwtMiddleware, h.activateSubscription)
	v1.Post("/provision", h.jwtMiddleware, h.autoProvision)
	v1.Get("/subscription/url", h.jwtMiddleware, h.getSubscriptionURL)
	v1.Post("/subscription/rotate", h.jwtMiddleware, h.rotateSubscription)

	v1.Get("/balance", h.jwtMiddleware, h.getBalance)
	v1.Post("/balance/topup", h.jwtMiddleware, h.topupBalance)
	v1.Post("/subscription/buy", h.jwtMiddleware, h.buyPlan)

	v1.Get("/traffic", h.jwtMiddleware, h.getTraffic)

	v1.Get("/referral", h.jwtMiddleware, h.getReferralCode)
	v1.Post("/referral/apply", h.jwtMiddleware, h.applyReferral)

	admin := v1.Group("/admin", h.jwtMiddleware, h.adminMiddleware)
	admin.Get("/users", h.adminListUsers)
	admin.Post("/plans", h.adminCreatePlan)
	admin.Post("/subscription/grant", h.adminGrantSubscription)
	admin.Post("/servers", h.adminCreateServer)
	admin.Get("/servers", h.adminListServers)
	admin.Get("/stats", h.adminStats)
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

	if req.ReferralCode != "" {
		referrer, refErr := h.db.GetUserByReferralCode(c.Context(), req.ReferralCode)
		if refErr == nil && referrer.ID != user.ID {
			if _, refErr := h.db.CreateReferral(c.Context(), referrer.ID, user.ID, referralBonusDays); refErr == nil {
				_ = h.provisioner.ExtendUserSubscription(c.Context(), referrer.ID, referralBonusDays)
				_ = h.provisioner.ExtendUserSubscription(c.Context(), user.ID, referralBonusDays)
			}
		}
	}

	token, err := auth.GenerateToken(user.ID, h.jwtSecret)
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
	token, err := auth.GenerateToken(user.ID, h.jwtSecret)
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

// --- Plans / subscription ---

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
	return c.JSON(fiber.Map{
		"version_code": versionCode,
		"version_name": versionName,
		"apk_url":      os.Getenv("APP_APK_URL"),
		"force_update": os.Getenv("APP_FORCE_UPDATE") == "true",
		"changelog":    os.Getenv("APP_CHANGELOG"),
	})
}

func (h *Handler) listPlans(c *fiber.Ctx) error {
	plans, err := h.db.ListPlans(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(plans)
}

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

	if _, err := h.db.GetActiveSubscription(c.Context(), userID); err == nil {
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
	url, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}
	return c.JSON(fiber.Map{"subscription": sub, "subscription_url": url})
}

func (h *Handler) autoProvision(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	sub, url, err := h.provisioner.ActivateFreePlanIfNone(c.Context(), user)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}
	return c.JSON(fiber.Map{"status": "ok", "subscription": sub, "subscription_url": url})
}

// --- Subscription URL + rotate ---

func (h *Handler) getSubscriptionURL(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	url, err := h.provisioner.GetSubscriptionURL(c.Context(), userID)
	if err != nil || url == "" {
		return fiber.NewError(fiber.StatusNotFound, "no subscription URL")
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"url":           url,
		"qr_png_base64": base64.StdEncoding.EncodeToString(png),
	})
}

func (h *Handler) rotateSubscription(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	url, err := h.provisioner.RotateSubscription(c.Context(), userID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "rotate failed")
	}
	return c.JSON(fiber.Map{"url": url})
}

// --- Referrals ---

const referralBonusDays = 3

func (h *Handler) getReferralCode(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
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

func (h *Handler) applyReferral(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	var req applyReferralRequest
	if err := c.BodyParser(&req); err != nil || req.Code == "" {
		return fiber.NewError(fiber.StatusBadRequest, "referral code required")
	}
	if existing, _ := h.db.GetReferralByReferred(c.Context(), userID); existing != nil {
		return fiber.NewError(fiber.StatusConflict, "referral already applied")
	}
	referrer, err := h.db.GetUserByReferralCode(c.Context(), req.Code)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "invalid referral code")
	}
	if referrer.ID == userID {
		return fiber.NewError(fiber.StatusBadRequest, "cannot use own referral code")
	}
	if _, err := h.db.CreateReferral(c.Context(), referrer.ID, userID, referralBonusDays); err != nil {
		return fiber.NewError(fiber.StatusConflict, "referral already applied")
	}
	_ = h.provisioner.ExtendUserSubscription(c.Context(), referrer.ID, referralBonusDays)
	_ = h.provisioner.ExtendUserSubscription(c.Context(), userID, referralBonusDays)
	return c.JSON(fiber.Map{
		"message":    fmt.Sprintf("Both you and the referrer received %d bonus days", referralBonusDays),
		"bonus_days": referralBonusDays,
	})
}

// --- Traffic ---

func (h *Handler) getTraffic(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	if _, err := h.db.GetActiveSubscription(c.Context(), userID); errors.Is(err, sql.ErrNoRows) {
		return fiber.NewError(fiber.StatusForbidden, "no active subscription")
	}
	used, limit, err := h.provisioner.GetTraffic(c.Context(), userID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get traffic")
	}
	return c.JSON(fiber.Map{"used_bytes": used, "limit_bytes": limit})
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
	return c.JSON(fiber.Map{"balance_kopecks": user.BalanceKopecks, "transactions": txs})
}

type buyPlanRequest struct {
	PlanID    string `json:"plan_id"`
	PromoCode string `json:"promo_code"`
}

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

	if _, err := h.db.GetActiveSubscription(c.Context(), userID); err == nil {
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
		_ = h.provisioner.ExtendUserSubscription(c.Context(), userID, bonusDays)
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

	url, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		log.Printf("provision failed: %v", err)
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}
	return c.JSON(fiber.Map{"subscription": sub, "subscription_url": url})
}

// --- Platega callback ---

type plategaCallbackBody struct {
	ID            string  `json:"id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Status        string  `json:"status"`
	PaymentMethod int     `json:"paymentMethod"`
	Payload       string  `json:"payload"`
}

// plategaCallback handles the server-to-server notification Platega sends when
// a transaction reaches a terminal state. Per Platega docs:
//   - Auth: X-MerchantId + X-Secret headers, no HMAC. We verify they match our
//     own merchant credentials (acts as shared secret).
//   - Status enum: CONFIRMED | CANCELED (CHARGEBACKED mentioned but not in enum).
//   - Must respond 200 within 60s; otherwise Platega retries 3x at 5-min intervals.
//   - `payload` carries our payments.id (set at /transaction/process time).
func (h *Handler) plategaCallback(c *fiber.Ctx) error {
	if h.plategaMerchantID == "" || h.plategaSecret == "" {
		return fiber.ErrServiceUnavailable
	}
	if c.Get("X-MerchantId") != h.plategaMerchantID || c.Get("X-Secret") != h.plategaSecret {
		log.Printf("platega callback: header auth mismatch")
		return fiber.ErrUnauthorized
	}

	var body plategaCallbackBody
	if err := c.BodyParser(&body); err != nil {
		return fiber.ErrBadRequest
	}

	// payload = our payment.id (UUID we generated at create-time).
	paymentID, err := uuid.Parse(body.Payload)
	if err != nil {
		log.Printf("platega callback: invalid payload %q", body.Payload)
		return fiber.ErrBadRequest
	}
	pmt, err := h.db.GetPaymentByID(c.Context(), paymentID)
	if err != nil {
		log.Printf("platega callback: payment %s not found", paymentID)
		// 200 anyway — otherwise Platega retries forever for a row we don't have.
		return c.SendString("OK")
	}
	if pmt.Status == "success" {
		return c.SendString("OK") // idempotent
	}

	switch strings.ToUpper(body.Status) {
	case "CONFIRMED":
		desc := fmt.Sprintf("Platega top-up %s", body.ID)
		if _, err := h.db.CreditBalance(c.Context(), pmt.UserID, pmt.AmountKopecks, "platega_topup", desc, nil); err != nil {
			log.Printf("platega callback: credit failed: %v", err)
			return fiber.ErrInternalServerError
		}
		_ = h.db.UpdatePaymentStatus(c.Context(), pmt.ID, "success", true)
	case "CANCELED", "CANCELLED", "FAILED", "REJECTED", "EXPIRED":
		_ = h.db.UpdatePaymentStatus(c.Context(), pmt.ID, "fail", false)
	default:
		// Unknown status — log and ack; poller will pick up next tick if needed.
		log.Printf("platega callback: unknown status %q for payment %s", body.Status, pmt.ID)
	}
	return c.SendString("OK")
}

// --- Top-up via Platega ---

type topupRequest struct {
	AmountKopecks int64 `json:"amount_kopecks"`
}

func (h *Handler) topupBalance(c *fiber.Ctx) error {
	if h.platega == nil || !h.platega.Configured() {
		return fiber.NewError(fiber.StatusServiceUnavailable, "payment provider not configured")
	}
	userID := c.Locals("userID").(uuid.UUID)
	var req topupRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.AmountKopecks < 10000 {
		return fiber.NewError(fiber.StatusBadRequest, "minimum amount is 100 RUB")
	}
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	pmt, err := h.db.CreatePayment(c.Context(), db.Payment{
		UserID: userID, Provider: "platega",
		AmountKopecks: req.AmountKopecks, Currency: "RUB", Status: "pending",
	})
	if err != nil {
		return fiber.ErrInternalServerError
	}

	payload := pmt.ID.String()
	resp, err := h.platega.CreatePayment(c.Context(),
		float64(req.AmountKopecks)/100,
		fmt.Sprintf("Top-up for %s", user.Email),
		h.plategaReturn, h.plategaFail, payload,
	)
	if err != nil {
		log.Printf("platega CreatePayment: %v", err)
		_ = h.db.UpdatePaymentStatus(c.Context(), pmt.ID, "fail", false)
		return fiber.NewError(fiber.StatusBadGateway, "payment gateway error")
	}
	if err := h.db.SetPaymentBillID(c.Context(), pmt.ID, resp.ID, resp.PayURL()); err != nil {
		return fiber.ErrInternalServerError
	}

	return c.JSON(fiber.Map{
		"payment_id":    pmt.ID,
		"pay_url":       resp.PayURL(),
		"amount_rubles": float64(req.AmountKopecks) / 100,
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
	return c.JSON(fiber.Map{"users": result, "limit": limit, "offset": offset})
}

func (h *Handler) adminListServers(c *fiber.Ctx) error {
	servers, err := h.db.ListServers(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	result := make([]fiber.Map, len(servers))
	for i, s := range servers {
		result[i] = fiber.Map{
			"id":                   s.ID,
			"name":                 s.Name,
			"type":                 s.Type,
			"country":              s.Country,
			"is_active":            s.IsActive,
			"remnawave_squad_uuid": s.RemnawaveSquadUUID,
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
	url, err := h.provisioner.Provision(c.Context(), user, sub)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "vpn provisioning failed")
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"subscription": sub, "subscription_url": url})
}

type createServerRequest struct {
	Name               string `json:"name"`
	RemnawaveSquadUUID string `json:"remnawave_squad_uuid"`
	Type               string `json:"type"`
	Country            string `json:"country"`
}

func (h *Handler) adminCreateServer(c *fiber.Ctx) error {
	var req createServerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.Type != "entry" && req.Type != "exit" {
		return fiber.NewError(fiber.StatusBadRequest, "type must be 'entry' or 'exit'")
	}
	squad, err := uuid.Parse(req.RemnawaveSquadUUID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid squad uuid")
	}
	server, err := h.db.CreateServer(c.Context(), db.Server{
		Name: req.Name, RemnawaveSquadUUID: squad,
		Type: req.Type, Country: req.Country, IsActive: true,
	})
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(server)
}

// --- Change password ---

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
	Kind      string  `json:"kind"`
	Value     int64   `json:"value"`
	MaxUses   *int    `json:"max_uses"`
	ExpiresAt *string `json:"expires_at"`
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
		tokenStr = c.Query("token")
	}
	if tokenStr == "" {
		return fiber.ErrUnauthorized
	}
	claims, err := auth.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
	}
	user, err := h.db.GetUserByID(c.Context(), claims.UserID)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "user not found")
	}
	c.Locals("userID", claims.UserID)
	c.Locals("role", user.Role)
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
