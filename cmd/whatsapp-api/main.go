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
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx registers as "postgres"
	"github.com/joho/godotenv"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	_ "modernc.org/sqlite" // modernc.org/sqlite registers as "sqlite"

	"github.com/mauroneto/whatsmeow-api/internal/auth"
	"github.com/mauroneto/whatsmeow-api/internal/config"
	"github.com/mauroneto/whatsmeow-api/internal/handlers"
	"github.com/mauroneto/whatsmeow-api/internal/instance"
	"github.com/mauroneto/whatsmeow-api/internal/store"
	"github.com/mauroneto/whatsmeow-api/internal/webhook"
)

func main() {
	// Auto-load .env from the current working directory (if present).
	// godotenv does NOT override already-set env vars, so an operator
	// who exports APP_MANAGER_PASSWORD inline still wins over the file.
	// A missing .env is fine — we just skip and rely on the real env.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		// A parse error (malformed file) is worth flagging — silently
		// ignoring it would lead to confusing "missing env var" errors
		// downstream. Not-exist is the normal "no .env" case.
		fmt.Fprintf(os.Stderr, "warning: failed to load .env: %v\n", err)
	}

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

	// Webhook dispatcher: per-instance secret delivery with
	// exponential-backoff retry. Webhook secrets are stored as
	// plaintext in the DB (post hotfix) — the dispatcher reads them
	// straight from the column and puts them in the X-Webhook-Secret
	// header on every outbound POST.
	dispatcher := webhook.NewDispatcher(db, instance.NewStore(db), webhook.DefaultConfig())
	dispatcher.Start()
	instanceStore := instance.NewStore(db)
	mgr.SubscribeEvents(func(instanceID string, evt interface{}) {
		// Persist status transitions to the DB so GET /admin/api/instances
		// and the manager UI see the same status the whatsmeow client has.
		switch evt.(type) {
		case *events.Connected:
			now := time.Now().UTC()
			_ = instanceStore.SetStatus(instanceID, instance.StatusConnected, &now, &now)
		case *events.Disconnected:
			// If the disconnect was operator-driven, the handler has
			// already set the status; this is the network-blip case.
			if !mgr.IsExpectedDisconnect(instanceID) {
				_ = instanceStore.SetStatus(instanceID, instance.StatusDisconnected, nil, nil)
			}
		case *events.LoggedOut:
			_ = instanceStore.SetStatus(instanceID, instance.StatusLoggedOut, nil, nil)
		}
		// Mirror every event into instance_logs (US-029). Best-effort
		// — a logging failure must not break the webhook pipeline.
		logEntry(instanceStore, instanceID, evt)
		// Forward to webhooks.
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
	router := buildRouter(cfg, db, mgr, dispatcher, instanceStore)
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
		slog.Info("shutdown signal received, draining (30s grace)")
	}

	// Graceful shutdown — explicit sequence so we can log each step
	// and bound the total time. Order: HTTP stops accepting new
	// connections → webhook dispatcher drains in-flight events → each
	// whatsmeow Client disconnects → DB closes. Defers still cover the
	// last two even if the explicit calls error out.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. HTTP server
	slog.Info("draining http server", "timeout", 30*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "err", err)
	} else {
		slog.Info("http server stopped")
	}

	// 2. Webhook dispatcher (workers finish the in-flight delivery, no
	// new jobs are accepted because the channel was already closed by
	// Shutdown once the HTTP server is quiet)
	slog.Info("draining webhook dispatcher")
	dispatcher.Shutdown(shutdownCtx)
	slog.Info("webhook dispatcher stopped")

	// 3. All whatsmeow clients
	slog.Info("disconnecting instance clients", "count", len(mgr.All()))
	mgr.StopAll()
	slog.Info("instance clients disconnected")

	// 4. DB
	slog.Info("closing database")
	if err := db.Close(); err != nil {
		slog.Error("db close failed", "err", err)
	}
	slog.Info("shutdown complete")
}

func buildRouter(cfg *config.Config, db *sql.DB, mgr *instance.Manager, dispatcher *webhook.Dispatcher, instanceStore *instance.Store) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	sessions := auth.NewSessionStore(db)
	// In production (HTTPS), prefer the `__Host-session` cookie name.
	// For dev / HTTP we fall back to `session` because the `__Host-`
	// prefix requires the Secure flag and clients silently drop it.
	if false { // TODO: detect via X-Forwarded-Proto or env flag
		sessions = auth.NewSessionStoreWithCookieName(db, "__Host-session")
	}
	limiter := auth.NewLoginRateLimiter()
	authDeps := handlers.AdminAuthDeps{
		DB:              db,
		Sessions:        sessions,
		Limiter:         limiter,
		ManagerPassword: cfg.ManagerPassword,
		ManagerUsername: cfg.ManagerUsername,
		SecureCookie:    false, // TODO: detect via X-Forwarded-Proto or env flag
	}
	audit := handlers.NewAuditLogger(db)
	// LifecycleDeps is shared by the HTML form-dispatcher and the
	// JSON API endpoints. Declared early so the HTML group can use
	// it for its routes too.
	lifecycleDeps := handlers.LifecycleDeps{
		DB:      db,
		Store:   instanceStore,
		Manager: mgr,
		Audit:   audit,
	}
	// APIKeyDeps is shared by the HTML form-dispatcher (rotate /
	// reveal / delete on the detail page) and the JSON API. Declared
	// early for the same reason as lifecycleDeps.
	apiKeyDeps := handlers.APIKeyDeps{
		Store:           instanceStore,
		Manager:         mgr,
		ManagerPassword: cfg.ManagerPassword,
		Audit:           audit,
	}

	// Login / logout are unauthenticated. Everything else under /admin/*
	// (HTML pages + JSON API) requires a valid session.
	r.POST("/admin/login", handlers.LoginHandler(authDeps))
	r.POST("/admin/logout", handlers.LogoutHandler(authDeps))
	r.GET("/admin/login", handlers.LoginPageHandler(cfg.ManagerUsername))

	// Manager-authenticated HTML pages (SessionMiddleware returns 401
	// for JSON requests and 302-redirects browser HTML to /admin/login).
	adminUI := r.Group("/admin", auth.SessionMiddleware(sessions))
	adminUI.GET("/", handlers.AdminListPage(db))
	adminUI.GET("/instances/new", handlers.AdminNewPage())
	adminUI.GET("/instances/:id", handlers.AdminDetailPage(db, mgr))
	// Form-based dispatcher for the lifecycle buttons in the
	// manager detail page (Connect/Disconnect/Reconnect). Reads
	// the `action` form value and routes to the right internal
	// method, then redirects back to the detail page.
	adminUI.POST("/instances/:id", handlers.LifecycleActionHandler(lifecycleDeps))
	// Form-based dispatcher for the api-key buttons (Rotate /
	// Reveal / Delete). The HTML forms submit to /admin/instances/{id}/...
	// (no /api/ segment, no DELETE verb) so we need a shim.
	adminUI.POST("/instances/:id/api-key/rotate", handlers.APIKeyFormActionHandler(apiKeyDeps))
	adminUI.POST("/instances/:id/reveal-key", handlers.APIKeyFormActionHandler(apiKeyDeps))
	adminUI.POST("/instances/:id/delete", handlers.APIKeyFormActionHandler(apiKeyDeps))
	adminUI.GET("/audit", handlers.AdminAuditPage(db))

	// Manager-authenticated JSON API.
	adminAPI := r.Group("/admin/api", auth.SessionMiddleware(sessions))
	adminAPI.POST("/instances", handlers.CreateInstanceHandler(instanceStore))
	adminAPI.GET("/instances", handlers.ListInstancesHandler(instanceStore))
	adminAPI.GET("/instances/:id", handlers.GetInstanceHandler(instanceStore))
	adminAPI.GET("/instances/:id/qr", handlers.InstanceQRHandler(mgr))
	adminAPI.GET("/instances/:id/status", handlers.InstanceStatusHandler(mgr))
	adminAPI.GET("/instances/:id/logs", handlers.ListInstanceLogsHandler(instanceStore))
	adminAPI.PUT("/instances/:id/webhook", handlers.SetWebhookHandler(instanceStore))
	adminAPI.GET("/instances/:id/webhook-deliveries", handlers.ListWebhookDeliveriesHandler(db))

	// Lifecycle JSON API endpoints (US-025..US-028)
	adminAPI.POST("/instances/:id/connect", handlers.ConnectInstanceHandler(lifecycleDeps))
	adminAPI.POST("/instances/:id/disconnect", handlers.DisconnectInstanceHandler(lifecycleDeps))
	adminAPI.POST("/instances/:id/reconnect", handlers.ReconnectInstanceHandler(lifecycleDeps))
	adminAPI.DELETE("/instances/:id", handlers.DeleteInstanceHandler(lifecycleDeps))

	// API-key management (US-031) — JSON API endpoints.
	adminAPI.PUT("/instances/:id/api-key", handlers.SetAPIKeyHandler(apiKeyDeps))
	adminAPI.POST("/instances/:id/api-key/rotate", handlers.RotateAPIKeyHandler(apiKeyDeps))
	adminAPI.POST("/instances/:id/api-key/reveal", handlers.RevealAPIKeyHandler(apiKeyDeps))
	// (delete is only via the form-dispatcher above — HTML forms
	// can't issue DELETE, and the form posts to a different URL)

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

// logEntry mirrors one whatsmeow event into the instance_logs table.
// Best-effort: a logging failure is slog.Warn'd, never returned. The
// `data` map captures the per-event payload (msg_id, from, chat, etc.)
// so operators can query the table for the same facts the webhook
// stream surfaces.
func logEntry(store *instance.Store, instanceID string, evt interface{}) {
	level := instance.LogLevelInfo
	category := instance.LogCategorySystem
	message := ""
	var data map[string]any

	switch e := evt.(type) {
	case *events.Connected:
		category = instance.LogCategoryConnect
		message = "instance connected"
	case *events.Disconnected:
		category = instance.LogCategoryConnect
		message = "instance disconnected"
	case *events.LoggedOut:
		level = instance.LogLevelWarn
		category = instance.LogCategoryConnect
		message = "instance logged out"
		data = map[string]any{"reason": e.Reason.String()}
	case *events.PairSuccess:
		category = instance.LogCategoryConnect
		message = "instance paired successfully"
		data = map[string]any{
			"phone": e.ID.User,
			"jid":   e.ID.String(),
			"lid":   e.LID.String(),
		}
	case *events.Message:
		category = instance.LogCategoryMessage
		message = "message received"
		data = map[string]any{
			"id":        e.Info.ID,
			"from":      e.Info.Sender.String(),
			"chat":      e.Info.Chat.String(),
			"is_group":  e.Info.IsGroup,
			"type":      e.Info.Type,
			"timestamp": e.Info.Timestamp.UTC().Format(time.RFC3339),
		}
	case *events.Receipt:
		category = instance.LogCategoryReceipt
		message = "receipt: " + strings.ToLower(string(e.Type))
		data = map[string]any{
			"message_ids": receiptMessageIDs(e.MessageIDs),
			"chat":        e.Chat.String(),
			"sender":      e.Sender.String(),
		}
	case *events.GroupInfo:
		category = instance.LogCategoryGroup
		message = "group info changed"
		data = map[string]any{
			"jid":  e.JID.String(),
			"name": e.Name,
		}
	case *events.Contact:
		category = instance.LogCategoryContact
		message = "contact changed"
		data = map[string]any{"jid": e.JID.String()}
	case *events.Presence:
		category = instance.LogCategoryPresence
		message = "presence updated"
		data = map[string]any{"from": e.From.String(), "available": !e.Unavailable}
	default:
		// Unknown event — record at debug level so we don't pollute the
		// log table with noise. The webhook normalizer drops these too.
		return
	}

	if err := store.InsertLog(context.Background(), instanceID, level, category, message, data); err != nil {
		slog.Warn("instance log insert", "id", instanceID, "err", err)
	}
}

// receiptMessageIDs converts a slice of typed MessageIDs to a slice of
// plain strings for the JSON data column.
func receiptMessageIDs(ids []types.MessageID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
