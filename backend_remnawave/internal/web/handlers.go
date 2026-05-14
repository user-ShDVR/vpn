package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/auth"
	"github.com/shdvr/vpn-backend/internal/captcha"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/disposable"
	"github.com/shdvr/vpn-backend/internal/email"
	"github.com/shdvr/vpn-backend/internal/payment"
	"github.com/shdvr/vpn-backend/internal/payment/platega"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/shdvr/vpn-backend/internal/remnawave"
	"github.com/shdvr/vpn-backend/web/templates"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "auth_token"
const referralBonusDays = 3

type Config struct {
	DB                 *db.DB
	Provisioner        *provisioner.Service
	JWTSecret          string
	PublicBaseURL      string
	StaticPath         string
	Platega            *platega.Client
	Poller             *payment.Poller
	PlategaReturnURL   string
	PlategaFailURL     string
	CookieSecure       bool
	Mailer             *email.Sender
	SupportTGURL       string
	SupportEmail       string
	SupportFAQURL      string
	RequireEmailVerify bool
	Remnawave          *remnawave.Client
	SubpageConfigUUID  string
}

type Handler struct {
	db                 *db.DB
	provisioner        *provisioner.Service
	jwtSecret          string
	publicBaseURL      string
	staticPath         string
	platega            *platega.Client
	poller             *payment.Poller
	plategaReturnURL   string
	plategaFailURL     string
	cookieSecure       bool
	mailer             *email.Sender
	supportTGURL       string
	supportEmail       string
	supportFAQURL      string
	requireEmailVerify bool
	rw                 *remnawave.Client
	subpageConfigUUID  string
}

func NewHandler(c Config) *Handler {
	return &Handler{
		db: c.DB, provisioner: c.Provisioner,
		jwtSecret: c.JWTSecret, publicBaseURL: c.PublicBaseURL,
		staticPath: c.StaticPath,
		platega:    c.Platega, poller: c.Poller,
		plategaReturnURL: c.PlategaReturnURL, plategaFailURL: c.PlategaFailURL,
		cookieSecure: c.CookieSecure,
		mailer:       c.Mailer,
		supportTGURL: c.SupportTGURL, supportEmail: c.SupportEmail, supportFAQURL: c.SupportFAQURL,
		requireEmailVerify: c.RequireEmailVerify,
		rw:                 c.Remnawave,
		subpageConfigUUID:  c.SubpageConfigUUID,
	}
}

func (h *Handler) Register(app *fiber.App) {
	app.Static("/static", h.staticPath)

	app.Get("/", h.rootRedirect)
	app.Get("/login", h.loginPage)
	app.Post("/login", h.loginSubmit)
	app.Get("/register", h.registerPage)
	app.Post("/register", h.registerSubmit)
	app.Post("/logout", h.logout)

	app.Get("/verify-email", h.verifyEmailHandler)
	app.Get("/forgot", h.forgotPage)
	app.Post("/forgot", h.forgotSubmit)
	app.Get("/reset", h.resetPage)
	app.Post("/reset", h.resetSubmit)

	app.Get("/privacy", h.privacyPage)
	app.Get("/terms", h.termsPage)

	// Platega redirect endpoints (no auth — Platega controls the URL the user lands on).
	app.Get("/payment/platega/return", h.plategaReturn)
	app.Get("/payment/platega/fail", h.plategaFail)

	g := app.Group("/", h.cookieAuth)

	a := g.Group("/admin", h.requireAdmin)
	a.Get("/", h.adminStatsPage)
	a.Get("/users", h.adminUsersPage)
	a.Get("/users/:id", h.adminUserDetailPage)
	a.Post("/users/:id/topup", h.adminUserTopupSubmit)
	a.Post("/users/:id/grant", h.adminUserGrantSubmit)
	a.Get("/servers", h.adminServersPage)
	a.Post("/servers", h.adminServerCreate)
	a.Post("/servers/:id/toggle", h.adminServerToggle)

	g.Post("/verify-email/resend", h.resendVerifyEmail)
	g.Get("/dashboard", h.dashboardPage)
	g.Get("/subscriptions", h.subscriptionsPage)
	g.Post("/subscriptions/refresh-traffic", h.refreshTraffic)
	g.Post("/subscriptions/devices/delete-all", h.deleteAllDevices)
	g.Post("/subscriptions/devices/:hwid/delete", h.deleteDevice)
	g.Post("/subscriptions/buy-extra-gb", h.buyExtraGB)
	g.Get("/connection", h.connectionPage)
	g.Post("/dashboard/rotate", h.rotateSubAndRender)
	g.Get("/subscription/purchase", h.purchasePage)
	g.Post("/subscription/purchase/:plan_id", h.buyPlanSubmit)
	g.Get("/balance", h.balancePage)
	g.Get("/balance/topup", h.topupPage)
	g.Get("/balance/topup/result", h.topupResultPage)
	g.Post("/balance/topup/:method", h.topupCreate)
	g.Post("/admin/topup", h.adminTopupSubmit)
	g.Get("/profile", h.profilePage)
	g.Post("/profile/password", h.changePasswordSubmit)
	g.Get("/referral", h.referralPage)
	g.Get("/support", h.supportPage)
}

// --- Legal / support / info ---

func (h *Handler) privacyPage(c *fiber.Ctx) error {
	return render(c, templates.Privacy(templates.LegalData{
		SupportEmail: h.supportEmail, UpdatedAt: "06.05.2026",
	}))
}

func (h *Handler) termsPage(c *fiber.Ctx) error {
	return render(c, templates.Terms(templates.LegalData{
		SupportEmail: h.supportEmail, UpdatedAt: "06.05.2026",
	}))
}

func (h *Handler) supportPage(c *fiber.Ctx) error {
	return render(c, templates.Support(templates.SupportData{
		TGURL: h.supportTGURL, Email: h.supportEmail, FAQURL: h.supportFAQURL,
	}))
}

// --- Helpers ---

func applyPromoFor(ctx context.Context, database *db.DB, userID uuid.UUID, planCost int64, code string, subID *uuid.UUID) (effective int64, bonusDays int, err error) {
	p, err := database.GetPromoCode(ctx, code)
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
	if _, err := database.RedeemPromo(ctx, userID, code, subID, discount, bonusDays); err != nil {
		return 0, 0, err
	}
	return planCost - discount, bonusDays, nil
}

func render(c *fiber.Ctx, comp templ.Component) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return comp.Render(c.Context(), c.Response().BodyWriter())
}

func (h *Handler) setAuthCookie(c *fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name: cookieName, Value: token,
		HTTPOnly: true, Secure: h.cookieSecure,
		SameSite: "Lax", Path: "/",
		MaxAge: 7 * 24 * 3600,
	})
}

func (h *Handler) clearAuthCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name: cookieName, Value: "",
		HTTPOnly: true, Path: "/", MaxAge: -1,
	})
}

func (h *Handler) hasAuthCookie(c *fiber.Ctx) bool {
	v := c.Cookies(cookieName)
	if v == "" {
		return false
	}
	_, err := auth.ParseToken(v, h.jwtSecret)
	return err == nil
}

// --- Middleware ---

func (h *Handler) cookieAuth(c *fiber.Ctx) error {
	tokenStr := c.Cookies(cookieName)
	if tokenStr == "" {
		return c.Redirect("/login", fiber.StatusFound)
	}
	claims, err := auth.ParseToken(tokenStr, h.jwtSecret)
	if err != nil {
		h.clearAuthCookie(c)
		return c.Redirect("/login", fiber.StatusFound)
	}
	user, err := h.db.GetUserByID(c.Context(), claims.UserID)
	if err != nil {
		h.clearAuthCookie(c)
		return c.Redirect("/login", fiber.StatusFound)
	}
	c.Locals("userID", claims.UserID)
	c.Locals("role", user.Role)
	return c.Next()
}

func (h *Handler) rootRedirect(c *fiber.Ctx) error {
	if h.hasAuthCookie(c) {
		return c.Redirect("/dashboard", fiber.StatusFound)
	}
	return c.Redirect("/login", fiber.StatusFound)
}

// --- Auth pages ---

func (h *Handler) loginPage(c *fiber.Ctx) error {
	if h.hasAuthCookie(c) {
		return c.Redirect("/dashboard", fiber.StatusFound)
	}
	return render(c, templates.Login(""))
}

func (h *Handler) loginSubmit(c *fiber.Ctx) error {
	email := c.FormValue("email")
	password := c.FormValue("password")
	if email == "" || password == "" {
		return render(c, templates.Login("Заполните email и пароль"))
	}
	user, err := h.db.GetUserByEmail(c.Context(), email)
	if err != nil {
		return render(c, templates.Login("Неверный email или пароль"))
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return render(c, templates.Login("Неверный email или пароль"))
	}
	token, err := auth.GenerateToken(user.ID, h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	h.setAuthCookie(c, token)
	return c.Redirect("/dashboard", fiber.StatusFound)
}

func (h *Handler) renderRegister(c *fiber.Ctx, errMsg, refCode string) error {
	chal, err := captcha.New(h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return render(c, templates.Register(templates.RegisterData{
		ErrMsg: errMsg, RefCode: refCode,
		CaptchaQuestion: chal.Question, CaptchaImage: chal.QuestionDataURL, CaptchaToken: chal.Token,
	}))
}

func (h *Handler) registerPage(c *fiber.Ctx) error {
	if h.hasAuthCookie(c) {
		return c.Redirect("/dashboard", fiber.StatusFound)
	}
	return h.renderRegister(c, "", c.Query("ref"))
}

func (h *Handler) registerSubmit(c *fiber.Ctx) error {
	emailAddr := c.FormValue("email")
	password := c.FormValue("password")
	refCode := c.FormValue("referral_code")
	captchaToken := c.FormValue("captcha_token")
	captchaAnswer := c.FormValue("captcha_answer")

	if err := captcha.Verify(h.jwtSecret, captchaToken, captchaAnswer); err != nil {
		msg := "Неверный ответ на проверку"
		if errors.Is(err, captcha.ErrCaptchaExpired) {
			msg = "Проверка истекла, попробуйте ещё раз"
		}
		return h.renderRegister(c, msg, refCode)
	}

	if _, err := mail.ParseAddress(emailAddr); err != nil {
		return h.renderRegister(c, "Неверный формат email", refCode)
	}
	if disposable.IsDisposable(emailAddr) {
		return h.renderRegister(c, "Используйте постоянный email-адрес", refCode)
	}
	if c.FormValue("accept_terms") != "1" {
		return h.renderRegister(c, "Примите условия соглашения", refCode)
	}
	if len(password) < 8 {
		return h.renderRegister(c, "Пароль минимум 8 символов", refCode)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	user, err := h.db.CreateUser(c.Context(), emailAddr, string(hash))
	if err != nil {
		return h.renderRegister(c, "Email уже зарегистрирован", refCode)
	}

	// Record the referral link only — the trial subscription and the
	// referrer's bonus days are granted later, once the user verifies
	// their email (see verifyEmailHandler). Pre-verification grants invite
	// throwaway-email farming.
	if refCode != "" {
		if referrer, err := h.db.GetUserByReferralCode(c.Context(), refCode); err == nil && referrer.ID != user.ID {
			_, _ = h.db.CreateReferral(c.Context(), referrer.ID, user.ID, referralBonusDays)
		}
	}

	h.sendVerifyEmail(c.Context(), user)

	token, err := auth.GenerateToken(user.ID, h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	h.setAuthCookie(c, token)
	return c.Redirect("/dashboard", fiber.StatusFound)
}

// --- Email verification ---

func (h *Handler) sendVerifyEmail(ctx context.Context, user *db.User) {
	tok, err := db.RandomToken(32)
	if err != nil {
		return
	}
	expires := time.Now().Add(24 * time.Hour)
	if err := h.db.SetEmailVerifyToken(ctx, user.ID, tok, expires); err != nil {
		return
	}
	link := h.publicBaseURL + "/verify-email?token=" + tok
	_ = h.mailer.Send(ctx, user.Email, "verify", "Подтвердите email — СвязьOK", email.TemplateData{
		UserEmail: user.Email, Link: link,
	})
}

func (h *Handler) verifyEmailHandler(c *fiber.Ctx) error {
	tok := c.Query("token")
	if tok == "" {
		return c.Redirect("/login", fiber.StatusFound)
	}
	user, err := h.db.GetUserByEmailVerifyToken(c.Context(), tok)
	if err != nil {
		return render(c, templates.Login("Ссылка недействительна или истекла"))
	}
	if err := h.db.MarkEmailVerified(c.Context(), user.ID); err != nil {
		return fiber.ErrInternalServerError
	}
	h.grantTrialSubscription(c.Context(), user)
	return c.Redirect("/dashboard?verified=1", fiber.StatusFound)
}

// grantTrialSubscription provisions the 1-day trial plan and gives the new
// user their referral bonus days (1 normal, 3 with referral). The
// REFERRER's bonus is deferred to the new user's first real payment — see
// ClaimReferralReward — to block throwaway-email farming.
func (h *Handler) grantTrialSubscription(ctx context.Context, user *db.User) {
	if _, _, err := h.provisioner.ActivateFreePlanIfNone(ctx, user); err != nil {
		log.Printf("trial provision failed for %s: %v", user.Email, err)
		return
	}
	ref, err := h.db.GetReferralByReferred(ctx, user.ID)
	if err != nil || ref == nil {
		return
	}
	// New user gets +bonusDays-1 (trial already gave 1 day; total = bonusDays).
	extra := ref.BonusDays - 1
	if extra > 0 {
		_ = h.provisioner.ExtendUserSubscription(ctx, user.ID, extra)
	}
}

func (h *Handler) resendVerifyEmail(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	if user.EmailVerified {
		return c.Redirect("/dashboard", fiber.StatusFound)
	}
	h.sendVerifyEmail(c.Context(), user)
	if c.Get("HX-Request") == "true" {
		return c.SendString(`<div class="rounded-2xl border border-success-500/30 bg-success-500/10 p-3 text-sm text-success-400">Письмо отправлено</div>`)
	}
	return c.Redirect("/dashboard?resent=1", fiber.StatusFound)
}

// --- Password reset ---

func (h *Handler) forgotPage(c *fiber.Ctx) error {
	return render(c, templates.Forgot("", false))
}

func (h *Handler) forgotSubmit(c *fiber.Ctx) error {
	addr := c.FormValue("email")
	user, err := h.db.GetUserByEmail(c.Context(), addr)
	if err == nil && user != nil {
		tok, _ := db.RandomToken(32)
		_ = h.db.SetPasswordResetToken(c.Context(), user.ID, tok, time.Now().Add(1*time.Hour))
		link := h.publicBaseURL + "/reset?token=" + tok
		_ = h.mailer.Send(c.Context(), user.Email, "reset", "Сброс пароля — СвязьOK", email.TemplateData{
			UserEmail: user.Email, Link: link,
		})
	}
	return render(c, templates.Forgot("", true))
}

func (h *Handler) resetPage(c *fiber.Ctx) error {
	tok := c.Query("token")
	if _, err := h.db.GetUserByPasswordResetToken(c.Context(), tok); err != nil {
		return render(c, templates.Login("Ссылка недействительна или истекла"))
	}
	return render(c, templates.Reset(tok, ""))
}

func (h *Handler) resetSubmit(c *fiber.Ctx) error {
	tok := c.FormValue("token")
	newp := c.FormValue("new_password")
	if len(newp) < 8 {
		return render(c, templates.Reset(tok, "Пароль минимум 8 символов"))
	}
	user, err := h.db.GetUserByPasswordResetToken(c.Context(), tok)
	if err != nil {
		return render(c, templates.Login("Ссылка недействительна или истекла"))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newp), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if err := h.db.UpdatePassword(c.Context(), user.ID, string(hash)); err != nil {
		return fiber.ErrInternalServerError
	}
	_ = h.db.ClearPasswordResetToken(c.Context(), user.ID)

	jwtTok, err := auth.GenerateToken(user.ID, h.jwtSecret)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	h.setAuthCookie(c, jwtTok)
	return c.Redirect("/dashboard", fiber.StatusFound)
}

func (h *Handler) logout(c *fiber.Ctx) error {
	h.clearAuthCookie(c)
	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/login")
		return c.SendStatus(fiber.StatusOK)
	}
	return c.Redirect("/login", fiber.StatusFound)
}

// --- Dashboard ---

func (h *Handler) dashboardPage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	sub, plan := h.activeSubAndPlan(c.Context(), userID)
	count, _ := h.db.CountReferrals(c.Context(), userID)
	trialDays := 1
	if ref, err := h.db.GetReferralByReferred(c.Context(), userID); err == nil && ref != nil && ref.BonusDays > 0 {
		trialDays = ref.BonusDays
	}
	return render(c, templates.Dashboard(templates.DashboardData{
		UserEmail:      user.Email,
		BalanceKopecks: user.BalanceKopecks,
		Subscription:   sub,
		Plan:           plan,
		ReferralCount:  count,
		EmailVerified:  user.EmailVerified,
		JustVerified:   c.Query("verified") == "1",
		JustResent:     c.Query("resent") == "1",
		TrialDays:      trialDays,
	}))
}

func (h *Handler) activeSubAndPlan(ctx context.Context, userID uuid.UUID) (*db.Subscription, *db.Plan) {
	sub, err := h.db.GetActiveSubscription(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil, nil
	}
	plan, _ := h.db.GetPlanByID(ctx, sub.PlanID)
	return sub, plan
}

// --- Subscriptions ---

func (h *Handler) subscriptionsPage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	sub, plan := h.activeSubAndPlan(c.Context(), userID)

	d := templates.SubscriptionsData{Subscription: sub, Plan: plan}
	if sub != nil {
		d.SubURL, _ = h.provisioner.GetSubscriptionURL(c.Context(), userID)
		d.QRPNGBase64 = encodeQR(d.SubURL)
		h.fillRemnawaveData(c.Context(), userID, &d)
	}
	if user, err := h.db.GetUserByID(c.Context(), userID); err == nil {
		d.BalanceKopecks = user.BalanceKopecks
	}
	if c.Query("gb_added") == "1" {
		d.Notice = "Доп. трафик добавлен"
	}
	switch c.Query("err") {
	case "balance":
		d.ErrMsg = "Недостаточно средств на балансе"
	case "gb_invalid":
		d.ErrMsg = "Минимум 10 ГБ, шагом 10 (10/20/30/…/1000)"
	case "no_extra":
		d.ErrMsg = "На вашем тарифе нельзя докупить трафик"
	case "panel":
		d.ErrMsg = "Не удалось обновить трафик на сервере. Деньги возвращены."
	case "no_sub":
		d.ErrMsg = "Нет активной подписки"
	}
	return render(c, templates.Subscriptions(d))
}

func encodeQR(url string) string {
	if url == "" {
		return ""
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(png)
}

// fillRemnawaveData populates traffic, reset strategy, squads, devices, and
// device limit from one /api/users + one /api/hwid/devices roundtrip. Used
// by both the full-page render and the HTMX partial refresh.
func (h *Handler) fillRemnawaveData(ctx context.Context, userID uuid.UUID, d *templates.SubscriptionsData) {
	u, _ := h.provisioner.GetRemnawaveUser(ctx, userID)
	if u != nil {
		d.UsedBytes = u.UsedTrafficBytes
		d.LimitBytes = u.TrafficLimitBytes
		d.ResetStrategy = u.TrafficLimitStrategy
		d.Squads = u.ActiveInternalSquads
		d.DeviceLimit = u.HwidDeviceLimit
	}
	devices, err := h.provisioner.ListUserDevices(ctx, userID)
	if err == nil {
		d.Devices = devices
	}
}

// refreshTraffic returns the updated traffic block as an HTMX partial.
func (h *Handler) refreshTraffic(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	d := templates.SubscriptionsData{}
	h.fillRemnawaveData(c.Context(), userID, &d)
	return render(c, templates.TrafficBlockPartial(d))
}

func (h *Handler) deleteDevice(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	hwid := c.Params("hwid")
	if hwid == "" {
		return fiber.ErrBadRequest
	}
	if err := h.provisioner.DeleteUserDevice(c.Context(), userID, hwid); err != nil {
		log.Printf("delete device: %v", err)
	}
	return h.renderDevicesCard(c, userID)
}

func (h *Handler) deleteAllDevices(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	devices, _ := h.provisioner.ListUserDevices(c.Context(), userID)
	for _, dev := range devices {
		if err := h.provisioner.DeleteUserDevice(c.Context(), userID, dev.HWID); err != nil {
			log.Printf("delete device %s: %v", dev.HWID, err)
		}
	}
	return h.renderDevicesCard(c, userID)
}

func (h *Handler) buyExtraGB(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	gb, err := strconv.Atoi(c.FormValue("gb"))
	if err != nil || gb < 10 || gb > 1000 || gb%10 != 0 {
		return c.Redirect("/subscriptions?err=gb_invalid", fiber.StatusFound)
	}
	sub, err := h.db.GetActiveSubscription(c.Context(), userID)
	if err != nil || sub == nil {
		return c.Redirect("/subscriptions?err=no_sub", fiber.StatusFound)
	}
	plan, err := h.db.GetPlanByID(c.Context(), sub.PlanID)
	if err != nil || plan.ExtraGBPriceKopecks <= 0 {
		return c.Redirect("/subscriptions?err=no_extra", fiber.StatusFound)
	}
	cost := int64(gb) * plan.ExtraGBPriceKopecks
	if _, err := h.db.DebitBalance(c.Context(), userID, cost, "extra_gb", fmt.Sprintf("Доп. трафик %d ГБ", gb), &sub.ID); err != nil {
		if errors.Is(err, db.ErrInsufficientBalance) {
			return c.Redirect("/subscriptions?err=balance", fiber.StatusFound)
		}
		return c.Redirect("/subscriptions?err=debit", fiber.StatusFound)
	}
	extraBytes := int64(gb) * 1024 * 1024 * 1024
	if err := h.provisioner.AddTraffic(c.Context(), userID, extraBytes); err != nil {
		log.Printf("add traffic failed user=%s gb=%d: %v", userID, gb, err)
		// Refund balance — provision failed.
		_, _ = h.db.CreditBalance(c.Context(), userID, cost, "extra_gb_refund", "Возврат: доп. трафик не добавлен", &sub.ID)
		return c.Redirect("/subscriptions?err=panel", fiber.StatusFound)
	}
	_ = h.db.CreateExtraGBPurchase(c.Context(), userID, &sub.ID, gb, cost)
	return c.Redirect("/subscriptions?gb_added=1", fiber.StatusFound)
}

func (h *Handler) renderDevicesCard(c *fiber.Ctx, userID uuid.UUID) error {
	d := templates.SubscriptionsData{}
	h.fillRemnawaveData(c.Context(), userID, &d)
	return render(c, templates.DevicesCard(d))
}

// connectionPage renders the Remnawave install-guide (platforms / apps /
// install blocks / deep-link buttons) inside our cabinet — bedolaga's
// approach ported to templ. Falls back gracefully if no subpage config UUID
// or panel call fails (e.g. token missing, panel down): shows the page
// header but with an empty-state notice.
func (h *Handler) connectionPage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)

	d := templates.ConnectionData{Lang: "ru"}

	subURL, _ := h.provisioner.GetSubscriptionURL(c.Context(), userID)
	d.SubscriptionURL = subURL

	if h.rw == nil || !h.rw.Configured() || h.subpageConfigUUID == "" {
		return render(c, templates.Connection(d))
	}

	cfg, err := h.rw.GetSubscriptionPageConfig(c.Context(), h.subpageConfigUUID)
	if err != nil {
		log.Printf("connection: get subpage config: %v", err)
		d.Error = "Не удалось загрузить инструкцию подключения. Попробуйте позже."
		return render(c, templates.Connection(d))
	}

	d.SvgLibrary = cfg.SvgLibrary
	d.BaseTranslations = cfg.BaseTranslations

	platformOrder := []string{"ios", "android", "windows", "macos", "linux", "androidTV", "appleTV"}
	platformNames := map[string]string{
		"ios": "iOS", "android": "Android", "windows": "Windows",
		"macos": "macOS", "linux": "Linux",
		"androidTV": "Android TV", "appleTV": "Apple TV",
	}
	detected := detectPlatformFromUA(c.Get("User-Agent"))

	for _, key := range platformOrder {
		p, ok := cfg.Platforms[key]
		if !ok || len(p.Apps) == 0 {
			continue
		}
		name := platformNames[key]
		if n := templates.PlatformDisplayName(p, d.Lang); n != "" {
			name = n
		}
		d.Platforms = append(d.Platforms, templates.PlatformOption{
			Key: key, Name: name, SvgIconKey: p.SvgIconKey,
		})
	}
	if len(d.Platforms) == 0 {
		return render(c, templates.Connection(d))
	}

	selectedKey := c.Query("platform")
	if !platformInList(d.Platforms, selectedKey) {
		if platformInList(d.Platforms, detected) {
			selectedKey = detected
		} else {
			selectedKey = d.Platforms[0].Key
		}
	}
	d.SelectedPlatform = selectedKey

	platform := cfg.Platforms[selectedKey]
	d.Apps = platform.Apps

	appIdx := 0
	if q := c.Query("app"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v >= 0 && v < len(d.Apps) {
			appIdx = v
		}
	} else {
		for i, a := range d.Apps {
			if a.Featured {
				appIdx = i
				break
			}
		}
	}
	d.SelectedAppIndex = appIdx
	d.SelectedApp = &d.Apps[appIdx]

	return render(c, templates.Connection(d))
}

func platformInList(list []templates.PlatformOption, key string) bool {
	if key == "" {
		return false
	}
	for _, p := range list {
		if p.Key == key {
			return true
		}
	}
	return false
}

func detectPlatformFromUA(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case strings.Contains(u, "iphone"), strings.Contains(u, "ipad"), strings.Contains(u, "ipod"):
		return "ios"
	case strings.Contains(u, "android"):
		if strings.Contains(u, "tv") {
			return "androidTV"
		}
		return "android"
	case strings.Contains(u, "macintosh"), strings.Contains(u, "mac os x"):
		return "macos"
	case strings.Contains(u, "windows"):
		return "windows"
	case strings.Contains(u, "linux"):
		return "linux"
	}
	return ""
}

func (h *Handler) rotateSubAndRender(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	url, err := h.provisioner.RotateSubscription(c.Context(), userID)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	sub, plan := h.activeSubAndPlan(c.Context(), userID)
	d := templates.SubscriptionsData{Subscription: sub, Plan: plan, SubURL: url, QRPNGBase64: encodeQR(url)}
	h.fillRemnawaveData(c.Context(), userID, &d)
	return render(c, templates.SubCard(d))
}

// --- Purchase ---

func (h *Handler) purchasePage(c *fiber.Ctx) error {
	return h.renderPurchase(c, "")
}

func (h *Handler) renderPurchase(c *fiber.Ctx, errMsg string) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	plans, err := h.db.ListPlans(c.Context())
	if err != nil {
		return fiber.ErrInternalServerError
	}
	currentSub, currentPlan := h.activeSubAndPlan(c.Context(), userID)
	return render(c, templates.Purchase(templates.PurchaseData{
		Plans: purchasablePlans(plans), BalanceKopecks: user.BalanceKopecks, ErrMsg: errMsg,
		CurrentPlan: currentPlan, CurrentSub: currentSub,
	}))
}

// purchasablePlans filters out the trial / zero-cost plans — those are
// granted automatically on email verification, never bought.
func purchasablePlans(plans []db.Plan) []db.Plan {
	out := plans[:0:0]
	for _, p := range plans {
		if p.CostKopecks > 0 {
			out = append(out, p)
		}
	}
	return out
}

func (h *Handler) buyPlanSubmit(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	planID, err := uuid.Parse(c.Params("plan_id"))
	if err != nil {
		return h.renderPurchase(c, "Некорректный тариф")
	}
	plan, err := h.db.GetPlanByID(c.Context(), planID)
	if err != nil {
		return h.renderPurchase(c, "Тариф не найден")
	}
	if plan.CostKopecks <= 0 {
		return h.renderPurchase(c, "Этот тариф нельзя купить")
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
		return h.renderPurchase(c, "Ошибка создания подписки")
	}

	promoCode := strings.TrimSpace(c.FormValue("promo_code"))
	effectiveCost := plan.CostKopecks
	if promoCode != "" {
		ec, bd, err := applyPromoFor(c.Context(), h.db, userID, plan.CostKopecks, promoCode, &sub.ID)
		if err != nil {
			_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
			return h.renderPurchase(c, "Промокод: "+err.Error())
		}
		effectiveCost = ec
		if bd > 0 {
			_ = h.provisioner.ExtendUserSubscription(c.Context(), userID, bd)
		}
	}

	if effectiveCost > 0 {
		if _, err := h.db.DebitBalance(c.Context(), userID, effectiveCost, "plan_purchase", plan.Name, &sub.ID); err != nil {
			_ = h.db.DeactivateUserSubscriptions(c.Context(), userID)
			if errors.Is(err, db.ErrInsufficientBalance) {
				return h.renderPurchase(c, "Недостаточно средств. Пополните баланс.")
			}
			return h.renderPurchase(c, "Ошибка списания")
		}
	}

	if _, err := h.provisioner.Provision(c.Context(), user, sub); err != nil {
		return h.renderPurchase(c, "Ошибка подключения к серверу связи: "+err.Error())
	}
	return c.Redirect("/subscriptions", fiber.StatusFound)
}

// --- Balance + Platega top-up ---

func (h *Handler) balancePage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	role, _ := c.Locals("role").(string)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	txs, _ := h.db.GetTransactionsByUser(c.Context(), userID, 50)

	notice, ok := "", false
	switch c.Query("paid") {
	case "1":
		notice, ok = "Оплата отправлена. Зачисление обычно занимает до 1 минуты.", true
	case "0":
		notice = "Платёж не прошёл. Попробуйте ещё раз."
	}
	if e := c.Query("err"); e != "" {
		notice = "Ошибка пополнения: " + e
	}

	return render(c, templates.Balance(templates.BalanceData{
		BalanceKopecks: user.BalanceKopecks, Transactions: txs,
		IsAdmin:        role == "admin",
		PaymentEnabled: h.platega != nil && h.platega.Configured(),
		Notice:         notice, NoticeOK: ok,
	}))
}

// --- Top-up: 3-step flow ---
//
// Step 1: GET  /balance/topup           → list of methods (СБП/Карты/Crypto/Universal)
// Step 2: GET  /balance/topup/:method   → amount form for selected method
// Step 3: POST /balance/topup/:method   → creates Platega invoice, redirects to pay
//         GET  /balance/topup/result    → success/fail screen after Platega return

var topupMethods = []templates.TopupMethod{
	{Slug: "sbp", Name: "🏦 СБП (QR)", IconKey: "sbp", Available: true},
	{Slug: "crypto", Name: "🪙 Криптовалюта", IconKey: "crypto", Available: true},
}

// plategaMethodID maps URL slugs to Platega numeric paymentMethod IDs.
// 0 = universal (v2 endpoint, Platega's own picker).
func plategaMethodID(slug string) int {
	switch slug {
	case "sbp":
		return 2
	case "erip":
		return 3
	case "cards":
		return 11
	case "intl":
		return 12
	case "crypto":
		return 13
	}
	return 0
}

func topupMethodBySlug(slug string) (templates.TopupMethod, bool) {
	for _, m := range topupMethods {
		if m.Slug == slug {
			return m, true
		}
	}
	return templates.TopupMethod{}, false
}

func (h *Handler) topupPage(c *fiber.Ctx) error {
	if h.platega == nil || !h.platega.Configured() {
		return c.Redirect("/balance?err=no_provider", fiber.StatusFound)
	}
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	selected := c.Query("method")
	if _, ok := topupMethodBySlug(selected); !ok {
		selected = topupMethods[0].Slug
	}
	errMsg := ""
	switch c.Query("err") {
	case "amount":
		errMsg = "Сумма должна быть от 100 до 1 000 000 ₽"
	case "gateway":
		errMsg = "Платежный шлюз недоступен. Попробуйте позже."
	case "db":
		errMsg = "Ошибка обработки. Попробуйте ещё раз."
	}
	return render(c, templates.Topup(templates.TopupData{
		Methods:        topupMethods,
		SelectedSlug:   selected,
		BalanceKopecks: user.BalanceKopecks,
		MinRub:         100,
		MaxRub:         1000000,
		Presets:        []int{100, 300, 500, 1000},
		ErrMsg:         errMsg,
	}))
}

func (h *Handler) topupCreate(c *fiber.Ctx) error {
	if h.platega == nil || !h.platega.Configured() {
		return c.Redirect("/balance?err=no_provider", fiber.StatusFound)
	}
	method, ok := topupMethodBySlug(c.Params("method"))
	if !ok {
		return c.Redirect("/balance/topup", fiber.StatusFound)
	}
	userID := c.Locals("userID").(uuid.UUID)
	rub, err := strconv.ParseFloat(c.FormValue("amount_rub"), 64)
	if err != nil || rub < 100 {
		return c.Redirect("/balance/topup/"+method.Slug+"?err=amount", fiber.StatusFound)
	}
	amountKopecks := int64(rub * 100)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	pmt, err := h.db.CreatePayment(c.Context(), db.Payment{
		UserID: userID, Provider: "platega",
		AmountKopecks: amountKopecks, Currency: "RUB", Status: "pending",
	})
	if err != nil {
		return c.Redirect("/balance/topup/"+method.Slug+"?err=db", fiber.StatusFound)
	}

	returnURL := h.plategaReturnURL
	if returnURL == "" {
		returnURL = h.publicBaseURL + "/payment/platega/return?invoice=" + pmt.ID.String()
	} else {
		returnURL = appendQuery(returnURL, "invoice", pmt.ID.String())
	}
	failURL := h.plategaFailURL
	if failURL == "" {
		failURL = h.publicBaseURL + "/payment/platega/fail?invoice=" + pmt.ID.String()
	} else {
		failURL = appendQuery(failURL, "invoice", pmt.ID.String())
	}

	resp, err := h.platega.CreatePaymentWithMethod(c.Context(),
		rub,
		fmt.Sprintf("Top-up balance for %s", user.Email),
		returnURL, failURL, pmt.ID.String(),
		plategaMethodID(method.Slug),
	)
	if err != nil {
		log.Printf("platega create: %v", err)
		_ = h.db.UpdatePaymentStatus(c.Context(), pmt.ID, "fail", false)
		return c.Redirect("/balance/topup/"+method.Slug+"?err=gateway", fiber.StatusFound)
	}
	if err := h.db.SetPaymentBillID(c.Context(), pmt.ID, resp.ID, resp.PayURL()); err != nil {
		return c.Redirect("/balance/topup/"+method.Slug+"?err=db", fiber.StatusFound)
	}
	return c.Redirect(resp.PayURL(), fiber.StatusFound)
}

func (h *Handler) topupResultPage(c *fiber.Ctx) error {
	d := templates.TopupResultData{
		Success: c.Query("status") == "success",
	}
	if invoiceStr := c.Query("invoice"); invoiceStr != "" {
		if id, err := uuid.Parse(invoiceStr); err == nil {
			if pmt, err := h.db.GetPaymentByID(c.Context(), id); err == nil {
				d.BillID = pmt.BillID
				if pmt.Status == "success" {
					d.Success = true
					d.AmountKopecks = pmt.AmountKopecks
				}
			}
		}
	}
	return render(c, templates.TopupResult(d))
}

// plategaReturn is hit by the browser when Platega redirects the user back.
// Re-checks the payment status synchronously so the user sees a fresh balance.
func (h *Handler) plategaReturn(c *fiber.Ctx) error {
	invoiceStr := c.Query("invoice")
	if invoiceStr != "" {
		if id, err := uuid.Parse(invoiceStr); err == nil && h.poller != nil {
			_ = h.poller.Reconcile(c.Context(), id)
		}
	}
	return c.Redirect("/balance?paid=1", fiber.StatusFound)
}

func (h *Handler) plategaFail(c *fiber.Ctx) error {
	invoiceStr := c.Query("invoice")
	if invoiceStr != "" {
		if id, err := uuid.Parse(invoiceStr); err == nil {
			_ = h.db.UpdatePaymentStatus(c.Context(), id, "fail", false)
		}
	}
	return c.Redirect("/balance?paid=0", fiber.StatusFound)
}

func (h *Handler) adminTopupSubmit(c *fiber.Ctx) error {
	role, _ := c.Locals("role").(string)
	if role != "admin" {
		return fiber.ErrForbidden
	}
	targetID, err := uuid.Parse(c.FormValue("user_id"))
	if err != nil {
		return c.Redirect("/balance", fiber.StatusFound)
	}
	amount, err := strconv.ParseInt(c.FormValue("amount_kopecks"), 10, 64)
	if err != nil || amount <= 0 {
		return c.Redirect("/balance", fiber.StatusFound)
	}
	_, _ = h.db.CreditBalance(c.Context(), targetID, amount, "admin_topup", "manual", nil)
	return c.Redirect("/balance", fiber.StatusFound)
}

// --- Profile ---

func (h *Handler) profilePage(c *fiber.Ctx) error {
	return h.renderProfile(c, "", "")
}

func (h *Handler) renderProfile(c *fiber.Ctx, errMsg, okMsg string) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	return render(c, templates.Profile(templates.ProfileData{
		Email: user.Email, Role: user.Role, ErrMsg: errMsg, OkMsg: okMsg,
	}))
}

func (h *Handler) changePasswordSubmit(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	cur := c.FormValue("current_password")
	newp := c.FormValue("new_password")
	if len(newp) < 8 {
		return h.renderProfile(c, "Новый пароль минимум 8 символов", "")
	}
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(cur)); err != nil {
		return h.renderProfile(c, "Текущий пароль неверный", "")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newp), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if err := h.db.UpdatePassword(c.Context(), userID, string(hash)); err != nil {
		return h.renderProfile(c, "Ошибка БД", "")
	}
	return h.renderProfile(c, "", "Пароль обновлён")
}

// --- Referral ---

func (h *Handler) referralPage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	if user.ReferralCode == nil {
		code := uuid.New().String()[:8]
		_ = h.db.SetReferralCode(c.Context(), userID, code)
		user.ReferralCode = &code
	}
	count, _ := h.db.CountReferrals(c.Context(), userID)
	list, _ := h.db.ListReferralsByReferrer(c.Context(), userID)
	bonus := 0
	for _, r := range list {
		bonus += r.BonusDays
	}
	return render(c, templates.Referral(templates.ReferralData{
		Code:      *user.ReferralCode,
		Link:      h.publicBaseURL + "/register?ref=" + *user.ReferralCode,
		Count:     count,
		BonusDays: bonus,
		List:      list,
	}))
}

// appendQuery appends a key=value pair to a URL, choosing ? or & correctly.
func appendQuery(url, key, value string) string {
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return url + sep + key + "=" + value
}
