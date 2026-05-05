package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/mail"
	"strconv"
	"time"

	"log"
	"strings"

	"github.com/a-h/templ"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/auth"
	"github.com/shdvr/vpn-backend/internal/captcha"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/disposable"
	"github.com/shdvr/vpn-backend/internal/email"
	"github.com/shdvr/vpn-backend/internal/payment/pally"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/shdvr/vpn-backend/web/templates"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "auth_token"
const referralBonusDays = 5

type Config struct {
	DB                 *db.DB
	Provisioner        *provisioner.Service
	JWTSecret          string
	PublicBaseURL      string
	StaticPath         string
	Pally              *pally.Client
	CookieSecure       bool
	Mailer             *email.Sender
	SupportTGURL       string
	SupportEmail       string
	SupportFAQURL      string
	RequireEmailVerify bool
}

type Handler struct {
	db                 *db.DB
	provisioner        *provisioner.Service
	jwtSecret          string
	publicBaseURL      string
	staticPath         string
	pally              *pally.Client
	cookieSecure       bool
	mailer             *email.Sender
	supportTGURL       string
	supportEmail       string
	supportFAQURL      string
	requireEmailVerify bool
}

func NewHandler(c Config) *Handler {
	return &Handler{
		db: c.DB, provisioner: c.Provisioner,
		jwtSecret: c.JWTSecret, publicBaseURL: c.PublicBaseURL,
		staticPath: c.StaticPath, pally: c.Pally,
		cookieSecure: c.CookieSecure,
		mailer:       c.Mailer,
		supportTGURL: c.SupportTGURL, supportEmail: c.SupportEmail, supportFAQURL: c.SupportFAQURL,
		requireEmailVerify: c.RequireEmailVerify,
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

	// Public auth flows
	app.Get("/verify-email", h.verifyEmailHandler)
	app.Get("/forgot", h.forgotPage)
	app.Post("/forgot", h.forgotSubmit)
	app.Get("/reset", h.resetPage)
	app.Post("/reset", h.resetSubmit)

	g := app.Group("/", h.cookieAuth)

	// Admin web cabinet (cookie-auth + admin role).
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
	g.Post("/dashboard/rotate", h.rotateSubAndRender)
	g.Get("/subscription/purchase", h.purchasePage)
	g.Post("/subscription/purchase/:plan_id", h.buyPlanSubmit)
	g.Get("/balance", h.balancePage)
	g.Post("/balance/topup", h.userTopupSubmit)
	g.Post("/admin/topup", h.adminTopupSubmit)
	g.Get("/profile", h.profilePage)
	g.Post("/profile/password", h.changePasswordSubmit)
	g.Get("/referral", h.referralPage)
	g.Get("/support", h.supportPage)
	g.Get("/info", h.infoPage)
}

func (h *Handler) supportPage(c *fiber.Ctx) error {
	return render(c, templates.Support(templates.SupportData{
		TGURL: h.supportTGURL, Email: h.supportEmail, FAQURL: h.supportFAQURL,
	}))
}

func (h *Handler) infoPage(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	return render(c, templates.Info(templates.InfoData{
		SubURL: h.publicBaseURL + "/sub/" + user.SubscriptionToken,
	}))
}

// --- Helpers ---

// applyPromoFor: shared between web + api handlers. Computes effective price
// and bonus days, atomically redeems the code.
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
	c.Locals("userID", claims.UserID)
	c.Locals("role", claims.Role)
	return c.Next()
}

// --- Root redirect ---

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
	token, err := auth.GenerateToken(user.ID, user.Role, h.jwtSecret)
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
		CaptchaQuestion: chal.Question, CaptchaToken: chal.Token,
	}))
}

func (h *Handler) registerPage(c *fiber.Ctx) error {
	if h.hasAuthCookie(c) {
		return c.Redirect("/dashboard", fiber.StatusFound)
	}
	return h.renderRegister(c, "", c.Query("ref"))
}

func (h *Handler) registerSubmit(c *fiber.Ctx) error {
	email := c.FormValue("email")
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

	if _, err := mail.ParseAddress(email); err != nil {
		return h.renderRegister(c, "Неверный формат email", refCode)
	}
	if disposable.IsDisposable(email) {
		return h.renderRegister(c, "Используйте постоянный email-адрес", refCode)
	}
	if len(password) < 8 {
		return h.renderRegister(c, "Пароль минимум 8 символов", refCode)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	user, err := h.db.CreateUser(c.Context(), email, string(hash))
	if err != nil {
		return h.renderRegister(c, "Email уже зарегистрирован", refCode)
	}

	if refCode != "" {
		if referrer, err := h.db.GetUserByReferralCode(c.Context(), refCode); err == nil && referrer.ID != user.ID {
			if _, err := h.db.CreateReferral(c.Context(), referrer.ID, user.ID, referralBonusDays); err == nil {
				_ = h.db.ExtendSubscription(c.Context(), referrer.ID, referralBonusDays)
				_ = h.db.ExtendSubscription(c.Context(), user.ID, referralBonusDays)
			}
		}
	}

	// Send email verification link (no-op in dev if SMTP not configured).
	h.sendVerifyEmail(c.Context(), user)

	// Activate Free plan + provision VPN (best-effort, ignore failure for new user UX).
	if _, _, err := h.provisioner.ActivateFreePlanIfNone(c.Context(), user); err != nil {
		// log only; user can retry via cabinet later
		log.Printf("free plan activation failed for %s: %v", user.Email, err)
	}

	token, err := auth.GenerateToken(user.ID, user.Role, h.jwtSecret)
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
	return c.Redirect("/dashboard?verified=1", fiber.StatusFound)
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
	// Always-200 to avoid email-existence leak.
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

	jwtTok, err := auth.GenerateToken(user.ID, user.Role, h.jwtSecret)
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

	return render(c, templates.Dashboard(templates.DashboardData{
		UserEmail:      user.Email,
		BalanceKopecks: user.BalanceKopecks,
		Subscription:   sub,
		Plan:           plan,
		ReferralCount:  count,
		EmailVerified:  user.EmailVerified,
		JustVerified:   c.Query("verified") == "1",
		JustResent:     c.Query("resent") == "1",
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
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	sub, plan := h.activeSubAndPlan(c.Context(), userID)

	d := templates.SubscriptionsData{Subscription: sub, Plan: plan}
	if sub != nil {
		d.SubURL = h.publicBaseURL + "/sub/" + user.SubscriptionToken
		if png, err := qrcode.Encode(d.SubURL, qrcode.Medium, 256); err == nil {
			d.QRPNGBase64 = base64.StdEncoding.EncodeToString(png)
		}
		d.Servers, _ = h.db.GetServerClientsWithNames(c.Context(), userID)
		d.OnlineDevices, _ = h.provisioner.GetOnlineDevices(c.Context(), userID)
	}

	return render(c, templates.Subscriptions(d))
}

func (h *Handler) rotateSubAndRender(c *fiber.Ctx) error {
	userID := c.Locals("userID").(uuid.UUID)
	if _, err := h.db.RotateSubscriptionToken(c.Context(), userID); err != nil {
		return fiber.ErrInternalServerError
	}
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	sub, plan := h.activeSubAndPlan(c.Context(), userID)
	d := templates.SubscriptionsData{
		Subscription: sub, Plan: plan,
		SubURL: h.publicBaseURL + "/sub/" + user.SubscriptionToken,
	}
	if png, err := qrcode.Encode(d.SubURL, qrcode.Medium, 256); err == nil {
		d.QRPNGBase64 = base64.StdEncoding.EncodeToString(png)
	}
	d.Servers, _ = h.db.GetServerClientsWithNames(c.Context(), userID)
	d.OnlineDevices, _ = h.provisioner.GetOnlineDevices(c.Context(), userID)
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
		Plans: plans, BalanceKopecks: user.BalanceKopecks, ErrMsg: errMsg,
		CurrentPlan: currentPlan, CurrentSub: currentSub,
	}))
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
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}

	if _, err := h.db.GetActiveSubscription(c.Context(), userID); err == nil {
		_ = h.provisioner.Deprovision(c.Context(), userID)
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
			_ = h.db.ExtendSubscription(c.Context(), userID, bd)
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
		return h.renderPurchase(c, "Ошибка подключения к серверу связи")
	}
	return c.Redirect("/subscriptions", fiber.StatusFound)
}

// --- Balance ---

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
		notice, ok = "Оплата отправлена. Зачисление на баланс происходит после подтверждения от Pally.", true
	case "0":
		notice = "Платёж не прошёл. Попробуйте ещё раз."
	}
	if e := c.Query("err"); e != "" {
		notice = "Ошибка пополнения: " + e
	}

	return render(c, templates.Balance(templates.BalanceData{
		BalanceKopecks: user.BalanceKopecks, Transactions: txs,
		IsAdmin:      role == "admin",
		PallyEnabled: h.pally != nil && h.pally.Configured(),
		Notice:       notice, NoticeOK: ok,
	}))
}

func (h *Handler) userTopupSubmit(c *fiber.Ctx) error {
	if h.pally == nil || !h.pally.Configured() {
		return c.Redirect("/balance?err=no_provider", fiber.StatusFound)
	}
	userID := c.Locals("userID").(uuid.UUID)
	rub, err := strconv.ParseFloat(c.FormValue("amount_rub"), 64)
	if err != nil || rub < 100 {
		return c.Redirect("/balance?err=amount", fiber.StatusFound)
	}
	amountKopecks := int64(rub * 100)
	user, err := h.db.GetUserByID(c.Context(), userID)
	if err != nil {
		return fiber.ErrNotFound
	}
	payment, err := h.db.CreatePayment(c.Context(), db.Payment{
		UserID: userID, Provider: "pally", BillID: "",
		AmountKopecks: amountKopecks, Currency: "RUB", Status: "pending",
	})
	if err != nil {
		return c.Redirect("/balance?err=db", fiber.StatusFound)
	}
	bill, err := h.pally.CreateBill(c.Context(), pally.CreateBillRequest{
		Amount:              rub,
		OrderID:             payment.ID.String(),
		Description:         "Top-up balance for " + user.Email,
		Custom:              payment.ID.String(),
		Name:                "СвязьOK",
		PayerPaysCommission: true,
	})
	if err != nil {
		_ = h.db.UpdatePaymentStatus(c.Context(), payment.ID, "fail", false)
		return c.Redirect("/balance?err=gateway", fiber.StatusFound)
	}
	if _, err := h.db.ExecContext(c.Context(),
		`UPDATE payments SET bill_id = $1, link_url = $2, pay_url = $3 WHERE id = $4`,
		bill.BillID, bill.LinkURL, bill.LinkPageURL, payment.ID,
	); err != nil {
		return c.Redirect("/balance?err=db", fiber.StatusFound)
	}
	return c.Redirect(bill.LinkPageURL, fiber.StatusFound)
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
