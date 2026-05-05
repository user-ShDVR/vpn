package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shdvr/vpn-backend/internal/api"
	"github.com/shdvr/vpn-backend/internal/db"
	"github.com/shdvr/vpn-backend/internal/email"
	"github.com/shdvr/vpn-backend/internal/payment/pally"
	"github.com/shdvr/vpn-backend/internal/provisioner"
	"github.com/shdvr/vpn-backend/internal/subscription"
	"github.com/shdvr/vpn-backend/internal/web"

	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL    string
	JWTSecret      string
	Port           string
	MigrationsPath string
	CORSOrigins    string
	PublicBaseURL  string
	WebStaticPath  string
	PallyToken     string
	PallyShopID    string
	PallyBaseURL   string
	CookieSecure   bool

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
	SMTPTLSMode  string

	SupportTGURL    string
	SupportEmail    string
	SupportFAQURL   string
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
		PallyToken:     getEnv("PALLY_TOKEN", ""),
		PallyShopID:    getEnv("PALLY_SHOP_ID", ""),
		PallyBaseURL:   getEnv("PALLY_BASE_URL", ""),
		CookieSecure:   getEnv("COOKIE_SECURE", "false") == "true",

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

// slogBridge routes stdlib log.Printf output to slog so we get one consistent
// log stream without rewriting 30+ log.Printf call sites.
type slogBridge struct{}

func (slogBridge) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	slog.Info(msg)
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

	// Init structured logger. Bridge stdlib log → slog so existing log.Printf
	// calls also surface as structured records.
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

	// Database
	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer database.Close()

	if err := db.RunMigrations(cfg.DatabaseURL, cfg.MigrationsPath); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Println("migrations applied")

	// Sync server client counts
	if err := database.SyncServerClientCounts(context.Background()); err != nil {
		log.Printf("warning: sync client counts: %v", err)
	}

	// Services
	prov := provisioner.New(database)
	scheduler := subscription.NewScheduler(database, prov)
	scheduler.Start()
	defer scheduler.Stop()

	// HTTP server
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

	// Liveness: process is up.
	app.Get("/live", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	// Readiness: process + DB.
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

	pallyCli := pally.New(cfg.PallyToken, cfg.PallyShopID, cfg.PallyBaseURL)
	mailer := email.New(email.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		User: cfg.SMTPUser, Pass: cfg.SMTPPass,
		From: cfg.SMTPFrom, TLSMode: cfg.SMTPTLSMode,
	})

	handler := api.NewHandler(database, prov, cfg.JWTSecret, cfg.PublicBaseURL, pallyCli)
	handler.Register(app)

	webHandler := web.NewHandler(web.Config{
		DB: database, Provisioner: prov, JWTSecret: cfg.JWTSecret,
		PublicBaseURL: cfg.PublicBaseURL, StaticPath: cfg.WebStaticPath,
		Pally: pallyCli, CookieSecure: cfg.CookieSecure,
		Mailer:             mailer,
		SupportTGURL:       cfg.SupportTGURL,
		SupportEmail:       cfg.SupportEmail,
		SupportFAQURL:      cfg.SupportFAQURL,
		RequireEmailVerify: cfg.RequireEmailVerify,
	})
	webHandler.Register(app)

	// Graceful shutdown
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
