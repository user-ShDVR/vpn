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
	return render(c, templates.AdminStats(templates.AdminStatsData{
		Users: users, ActiveSubscriptions: subs, ActiveServers: servers,
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
	return h.renderUserDetail(c, fmt.Sprintf("Зачислено %.2f ₽", rub), "")
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
	squad, err := uuid.Parse(strings.TrimSpace(c.FormValue("remnawave_squad_uuid")))
	if err != nil {
		return h.renderServers(c, "", "Некорректный squad UUID")
	}
	srv := db.Server{
		Name:               strings.TrimSpace(c.FormValue("name")),
		RemnawaveSquadUUID: squad,
		Type:               c.FormValue("type"),
		Country:            strings.TrimSpace(c.FormValue("country")),
		IsActive:           true,
	}
	if srv.Type != "entry" && srv.Type != "exit" {
		return h.renderServers(c, "", "type must be entry|exit")
	}
	if srv.Name == "" {
		return h.renderServers(c, "", "name required")
	}
	created, err := h.db.CreateServer(c.Context(), srv)
	if err != nil {
		return h.renderServers(c, "", "Ошибка БД: "+err.Error())
	}
	return h.renderServers(c, "Сервер "+created.Name+" добавлен", "")
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
	_ = h.db.UpdateServerActive(c.Context(), id, !srv.IsActive)
	return c.Redirect("/admin/servers", fiber.StatusFound)
}
