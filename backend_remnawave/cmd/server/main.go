package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shdvr/vpn-backend/internal/api"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/email"
	"github.com/shdvr/vpn-backend/internal/payment"
	"github.com/shdvr/vpn-backend/internal/payment/platega"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/shdvr/vpn-backend/internal/remnawave"
	"github.com/shdvr/vpn-backend/internal/subscription"
	"github.com/shdvr/vpn-backend/internal/web"
	"github.com/shdvr/vpn-backend/web/templates"
)

type Config struct {
	DatabaseURL    string
	JWTSecret      string
	Port           string
	MigrationsPath string
	CORSOrigins    string
	PublicBaseURL  string
	WebStaticPath  string

	RemnawaveBaseURL        string
	RemnawaveToken          string
	RemnawaveSubpageConfigUUID string

	PlategaBaseURL       string
	PlategaMerchantID    string
	PlategaSecret        string
	PlategaPaymentMethod int
	PlategaReturnURL     string
	PlategaFailURL       string

	CookieSecure bool

	SMTPHost    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string
	SMTPFrom    string
	SMTPTLSMode string

	SupportTGURL       string
	SupportEmail       string
	SupportFAQURL      string
	RequireEmailVerify bool
}

func loadConfig() Config {
	return Config{
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://vpn:vpn@localhost:5432/vpn?sslmode=disable"),
		JWTSecret:      requireEnv("JWT_SECRET"),
		Port:           getEnv("PORT", "8080"),
		MigrationsPath: getEnv("MIGRATIONS_PATH", "./migrations"),
		CORSOrigins:    getEnv("CORS_ORIGINS", "*"),
		PublicBaseURL:  getEnv("PUBLIC_BASE_URL", "http://localhost:8080"),
		WebStaticPath:  getEnv("WEB_STATIC_PATH", "./web/static"),

		RemnawaveBaseURL:           getEnv("REMNAWAVE_BASE_URL", ""),
		RemnawaveToken:             getEnv("REMNAWAVE_TOKEN", ""),
		RemnawaveSubpageConfigUUID: getEnv("REMNAWAVE_SUBPAGE_CONFIG_UUID", ""),

		PlategaBaseURL:       getEnv("PLATEGA_BASE_URL", ""),
		PlategaMerchantID:    getEnv("PLATEGA_MERCHANT_ID", ""),
		PlategaSecret:        getEnv("PLATEGA_SECRET", ""),
		PlategaPaymentMethod: atoiOr(getEnv("PLATEGA_PAYMENT_METHOD", "1"), 1),
		PlategaReturnURL:     getEnv("PLATEGA_RETURN_URL", ""),
		PlategaFailURL:       getEnv("PLATEGA_FAIL_URL", ""),

		CookieSecure: getEnv("COOKIE_SECURE", "false") == "true",

		SMTPHost:    getEnv("SMTP_HOST", ""),
		SMTPPort:    atoiOr(getEnv("SMTP_PORT", "587"), 587),
		SMTPUser:    getEnv("SMTP_USER", ""),
		SMTPPass:    getEnv("SMTP_PASS", ""),
		SMTPFrom:    getEnv("SMTP_FROM", "noreply@svyaz-ok.example"),
		SMTPTLSMode: getEnv("SMTP_TLS_MODE", "starttls"),

		SupportTGURL:       getEnv("SUPPORT_TG_URL", ""),
		SupportEmail:       getEnv("SUPPORT_EMAIL", ""),
		SupportFAQURL:      getEnv("SUPPORT_FAQ_URL", ""),
		RequireEmailVerify: getEnv("REQUIRE_EMAIL_VERIFY", "false") == "true",
	}
}

func atoiOr(s string, fallback int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return fallback
}

type slogBridge struct{}

func (slogBridge) Write(p []byte) (int, error) {
	slog.Info(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func main() {
	cfg := loadConfig()

	templates.YandexMetrikaID = getEnv("YANDEX_METRIKA_ID", "")

	level := slog.LevelInfo
	switch strings.ToLower(getEnv("LOG_LEVEL", "info")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var slogH slog.Handler
	if strings.ToLower(getEnv("LOG_FORMAT", "text")) == "json" {
		slogH = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		slogH = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(slogH))
	log.SetFlags(0)
	log.SetOutput(slogBridge{})

	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer database.Close()

	if err := db.RunMigrations(cfg.DatabaseURL, cfg.MigrationsPath); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Println("migrations applied")

	rw := remnawave.New(cfg.RemnawaveBaseURL, cfg.RemnawaveToken)
	if !rw.Configured() {
		log.Println("WARNING: Remnawave client not configured — provisioning calls will fail")
	}

	prov := provisioner.New(database, rw)
	scheduler := subscription.NewScheduler(database, prov)
	scheduler.Start()
	defer scheduler.Stop()

	plategaCli := platega.New(cfg.PlategaMerchantID, cfg.PlategaSecret, cfg.PlategaBaseURL, cfg.PlategaPaymentMethod)
	poller := payment.NewPoller(database, plategaCli)
	poller.Start()
	defer poller.Stop()

	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			msg := "internal server error"
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
				msg = e.Message
			}
			return c.Status(code).JSON(fiber.Map{"error": msg})
		},
	})

	app.Use(recover.New())
	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: cfg.CORSOrigins,
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	app.Get("/live", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	app.Get("/health", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()
		if err := database.PingContext(ctx); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "db_down", "error": err.Error(),
			})
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})

	mailer := email.New(email.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		User: cfg.SMTPUser, Pass: cfg.SMTPPass,
		From: cfg.SMTPFrom, TLSMode: cfg.SMTPTLSMode,
	})

	apiHandler := api.NewHandler(api.Config{
		DB: database, Provisioner: prov,
		JWTSecret: cfg.JWTSecret, PublicBaseURL: cfg.PublicBaseURL,
		Platega:           plategaCli,
		Poller:            poller,
		PlategaReturn:     cfg.PlategaReturnURL,
		PlategaFail:       cfg.PlategaFailURL,
		PlategaMerchantID: cfg.PlategaMerchantID,
		PlategaSecret:     cfg.PlategaSecret,
	})
	apiHandler.Register(app)

	webHandler := web.NewHandler(web.Config{
		DB: database, Provisioner: prov,
		JWTSecret: cfg.JWTSecret, PublicBaseURL: cfg.PublicBaseURL,
		StaticPath:       cfg.WebStaticPath,
		Platega:          plategaCli,
		Poller:           poller,
		PlategaReturnURL: cfg.PlategaReturnURL,
		PlategaFailURL:   cfg.PlategaFailURL,
		CookieSecure:     cfg.CookieSecure,
		Mailer:           mailer,
		SupportTGURL:     cfg.SupportTGURL,
		SupportEmail:     cfg.SupportEmail,
		SupportFAQURL:    cfg.SupportFAQURL,
		RequireEmailVerify: cfg.RequireEmailVerify,
		Remnawave:        rw,
		SubpageConfigUUID: cfg.RemnawaveSubpageConfigUUID,
	})
	webHandler.Register(app)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-quit
		log.Println("shutting down...")
		_ = app.Shutdown()
	}()

	log.Printf("starting server on :%s", cfg.Port)
	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
