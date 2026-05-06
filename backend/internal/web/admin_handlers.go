package web

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/web/templates"
)

func (h *Handler) requireAdmin(c *fiber.Ctx) error {
	role, _ := c.Locals("role").(string)
	if role != "admin" {
		return fiber.ErrForbidden
	}
	return c.Next()
}

func (h *Handler) adminStatsPage(c *fiber.Ctx) error {
	users, _ := h.db.CountUsers(c.Context())
	subs, _ := h.db.CountActiveSubscriptions(c.Context())
	servers, _ := h.db.CountServers(c.Context())
	syncMsg := ""
	if s := c.Query("synced"); s != "" {
		syncMsg = fmt.Sprintf("Синхронизировано: %s, ошибок: %s", s, c.Query("failed", "0"))
	}
	return render(c, templates.AdminStats(templates.AdminStatsData{
		Users: users, ActiveSubscriptions: subs, ActiveServers: servers,
		SyncMsg: syncMsg,
	}))
}

func (h *Handler) adminUsersPage(c *fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := strings.TrimSpace(c.Query("q"))
	users, _ := h.searchUsers(c, q, limit, offset)
	return render(c, templates.AdminUsers(templates.AdminUsersData{
		Users: users, Search: q, Limit: limit, Offset: offset,
	}))
}

func (h *Handler) searchUsers(c *fiber.Ctx, q string, limit, offset int) ([]db.User, error) {
	if q == "" {
		return h.db.ListUsers(c.Context(), limit, offset)
	}
	var users []db.User
	err := h.db.SelectContext(c.Context(), &users,
		`SELECT * FROM users WHERE email ILIKE $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		"%"+q+"%", limit, offset,
	)
	return users, err
}

func (h *Handler) adminUserDetailPage(c *fiber.Ctx) error {
	return h.renderUserDetail(c, "", "")
}

func (h *Handler) renderUserDetail(c *fiber.Ctx, okMsg, errMsg string) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	user, err := h.db.GetUserByID(c.Context(), id)
	if err != nil {
		return fiber.ErrNotFound
	}
	sub, plan := h.activeSubAndPlan(c.Context(), id)
	plans, _ := h.db.ListPlans(c.Context())
	txs, _ := h.db.GetTransactionsByUser(c.Context(), id, 50)
	return render(c, templates.AdminUserDetail(templates.AdminUserDetailData{
		User: user, Subscription: sub, Plan: plan, Plans: plans,
		Transactions: txs, OkMsg: okMsg, ErrMsg: errMsg,
	}))
}

func (h *Handler) adminUserTopupSubmit(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	rub, err := strconv.ParseFloat(c.FormValue("amount_rub"), 64)
	if err != nil || rub <= 0 {
		return h.renderUserDetail(c, "", "Сумма должна быть положительной")
	}
	desc := strings.TrimSpace(c.FormValue("description"))
	if desc == "" {
		desc = "manual top-up"
	}
	amount := int64(rub * 100)
	if _, err := h.db.CreditBalance(c.Context(), id, amount, "admin_topup", desc, nil); err != nil {
		return h.renderUserDetail(c, "", "Ошибка зачисления: "+err.Error())
	}
	return h.renderUserDetail(c, "Зачислено "+strconv.FormatFloat(rub, 'f', 2, 64)+" ₽", "")
}

func (h *Handler) adminUserGrantSubmit(c *fiber.Ctx) error {
	userID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	planID, err := uuid.Parse(c.FormValue("plan_id"))
	if err != nil {
		return h.renderUserDetail(c, "", "Не выбран тариф")
	}
	plan, err := h.db.GetPlanByID(c.Context(), planID)
	if err != nil {
		return h.renderUserDetail(c, "", "Тариф не найден")
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
		return h.renderUserDetail(c, "", "Ошибка создания подписки")
	}
	if _, err := h.provisioner.Provision(c.Context(), user, sub); err != nil {
		return h.renderUserDetail(c, "", "Provision: "+err.Error())
	}
	return h.renderUserDetail(c, "Тариф "+plan.Name+" выдан", "")
}

func (h *Handler) adminServersPage(c *fiber.Ctx) error {
	return h.renderServers(c, "", "")
}

func (h *Handler) renderServers(c *fiber.Ctx, okMsg, errMsg string) error {
	servers, _ := h.db.ListServers(c.Context())
	return render(c, templates.AdminServers(templates.AdminServersData{
		Servers: servers, OkMsg: okMsg, ErrMsg: errMsg,
	}))
}

func (h *Handler) adminServerCreate(c *fiber.Ctx) error {
	port, _ := strconv.Atoi(c.FormValue("port"))
	inboundID, _ := strconv.Atoi(c.FormValue("inbound_id"))
	maxClients, _ := strconv.Atoi(c.FormValue("max_clients"))
	if maxClients <= 0 {
		maxClients = 200
	}
	subPath := c.FormValue("sub_path")
	if subPath == "" {
		subPath = "/sub/"
	}
	srv := db.Server{
		Name:       c.FormValue("name"),
		PanelURL:   c.FormValue("panel_url"),
		PanelUser:  c.FormValue("panel_user"),
		PanelPass:  c.FormValue("panel_pass"),
		InboundID:  inboundID,
		Type:       c.FormValue("type"),
		Host:       c.FormValue("host"),
		Port:       port,
		SubURL:     c.FormValue("sub_url"),
		SubPath:    subPath,
		MaxClients: maxClients,
	}
	if srv.Type != "entry" && srv.Type != "exit" {
		return h.renderServers(c, "", "type must be entry|exit")
	}
	created, err := h.db.CreateServer(c.Context(), srv)
	if err != nil {
		return h.renderServers(c, "", "Ошибка БД: "+err.Error())
	}
	if maxClients != 200 {
		_ = h.db.UpdateServer(c.Context(), created.ID, nil, &maxClients)
	}
	return h.renderServers(c, "Сервер "+created.Name+" добавлен", "")
}

// adminSyncExpiries pushes current DB sub.expires_at to every xray client of
// every active subscription. Run once after migration 015 so migrated Free
// users actually keep their VPN access for the new 30-day period.
func (h *Handler) adminSyncExpiries(c *fiber.Ctx) error {
	rows, err := h.db.QueryxContext(c.Context(), `
		SELECT DISTINCT user_id FROM subscriptions WHERE is_active = TRUE AND expires_at > NOW()
	`)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()

	synced := 0
	failed := 0
	for rows.Next() {
		var uid uuid.UUID
		if err := rows.Scan(&uid); err != nil {
			continue
		}
		if err := h.provisioner.ExtendUserSubscription(c.Context(), uid, 0); err != nil {
			failed++
		} else {
			synced++
		}
	}
	return c.Redirect(fmt.Sprintf("/admin?synced=%d&failed=%d", synced, failed), fiber.StatusFound)
}

func (h *Handler) adminServerToggle(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	srv, err := h.db.GetServerByID(c.Context(), id)
	if err != nil {
		return fiber.ErrNotFound
	}
	flip := !srv.IsActive
	_ = h.db.UpdateServer(c.Context(), id, &flip, nil)
	return c.Redirect("/admin/servers", fiber.StatusFound)
}
