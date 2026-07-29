// Command whatsapp-api is the entry point for the whatsmeow REST API service.
// It embeds the whatsmeow library and exposes a multi-instance HTTP surface
// plus a manager UI gated by an env-supplied password.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"


	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx registers as "postgres"
	_ "modernc.org/sqlite"             // modernc.org/sqlite registers as "sqlite"

	"github.com/mauroneto/whatsmeow-api/internal/auth"
	"github.com/mauroneto/whatsmeow-api/internal/config"
	"github.com/mauroneto/whatsmeow-api/internal/handlers"
	"github.com/mauroneto/whatsmeow-api/internal/instance"
	"github.com/mauroneto/whatsmeow-api/internal/store"
	"github.com/mauroneto/whatsmeow-api/internal/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	slog.Info("manager panel ready",
		"user", cfg.ManagerUsername,
		"listen", cfg.HTTPAddr,
		"db_driver", cfg.DBDriver,
	)

	// Open DB + run migrations.
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 30*time.Second)
	db, err := store.Open(bootCtx, cfg.DBDriver, cfg.DBDSN)
	cancelBoot()
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := store.Migrate(db, cfg.DBDriver); err != nil {
		slog.Error("migrate db", "err", err)
		os.Exit(1)
	}
	slog.Info("database ready", "driver", cfg.DBDriver)

	// Build the instance manager: opens the whatsmeow sqlstore over the
	// same DB, loads every row, and starts a Client per instance. Uses
	// context.Background() (not bootCtx) because we cancel that as soon
	// as Open returns.
	mgr, err := instance.NewManager(db, store.SQLDriverName(cfg.DBDriver))
	if err != nil {
		slog.Error("init instance manager", "err", err)
		os.Exit(1)
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	if err := mgr.StartAll(startCtx); err != nil {
		cancelStart()
		slog.Error("start instances", "err", err)
		os.Exit(1)
	}
	cancelStart()
	slog.Info("instance manager ready", "count", len(mgr.All()))
	defer mgr.StopAll()

	// Webhook dispatcher: per-instance encrypted secret delivery with
	// exponential-backoff retry.
	dispatcher := webhook.NewDispatcher(db, instance.NewStore(db), cfg.EncryptionKey, webhook.DefaultConfig())
	dispatcher.Start()
	defer dispatcher.Shutdown(context.Background())
	mgr.SubscribeEvents(func(instanceID string, evt interface{}) {
		ev, ok := webhook.Normalize(instanceID, evt)
		if !ok {
			return
		}
		payload, _ := json.Marshal(ev)
		deliveryID, err := dispatcher.RecordDelivery(instanceID, ev.Event, payload)
		if err != nil {
			slog.Warn("record delivery", "id", instanceID, "err", err)
			return
		}
		dispatcher.Enqueue(instanceID, ev.Event, deliveryID, ev)
	})

	_ = cfg // silence
	router := buildRouter(cfg, db, mgr, dispatcher)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		slog.Error("http server failed", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}

func buildRouter(cfg *config.Config, db *sql.DB, mgr *instance.Manager, dispatcher *webhook.Dispatcher) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	sessions := auth.NewSessionStore(db)
	limiter := auth.NewLoginRateLimiter()
	authDeps := handlers.AdminAuthDeps{
		DB:              db,
		Sessions:        sessions,
		Limiter:         limiter,
		ManagerPassword: cfg.ManagerPassword,
		ManagerUsername: cfg.ManagerUsername,
		SecureCookie:    false, // TODO: detect via X-Forwarded-Proto or env flag
	}
	r.POST("/admin/login", handlers.LoginHandler(authDeps))
	r.POST("/admin/logout", handlers.LogoutHandler(authDeps))
	r.GET("/admin/login", handlers.LoginPageHandler(cfg.ManagerUsername))
	r.GET("/admin/", handlers.AdminListPage(db))
	r.GET("/admin/instances/new", handlers.AdminNewPage())
	r.GET("/admin/instances/:id", handlers.AdminDetailPage(db))
	r.GET("/admin/audit", handlers.AdminAuditPage(db))

	instanceStore := instance.NewStore(db)
	r.POST("/admin/api/instances", handlers.CreateInstanceHandler(instanceStore))
	r.GET("/admin/api/instances", handlers.ListInstancesHandler(instanceStore))
	r.GET("/admin/api/instances/:id", handlers.GetInstanceHandler(instanceStore))
	r.GET("/admin/api/instances/:id/qr", handlers.InstanceQRHandler(mgr))
	r.GET("/admin/api/instances/:id/status", handlers.InstanceStatusHandler(mgr))
	r.PUT("/admin/api/instances/:id/webhook", handlers.SetWebhookHandler(instanceStore, cfg.EncryptionKey))
	r.GET("/admin/api/instances/:id/webhook-deliveries", handlers.ListWebhookDeliveriesHandler(db))

	// Swagger UI + raw OpenAPI spec (US-017 + US-018)
	r.GET("/swagger", handlers.SwaggerUIHandler())
	r.GET("/swagger/openapi.yaml", handlers.OpenAPISpecHandler())

	// Per-instance API-key routes (Bearer auth)
	api := r.Group("/api/v1", handlers.InstanceAPIKeyAuth(mgr))
	api.GET("/ping", handlers.PingHandler())
	api.POST("/messages/text", handlers.SendTextHandler())
	api.POST("/messages/buttons", handlers.SendButtonsHandler())

	return r
}
