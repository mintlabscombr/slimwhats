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
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx registers as "postgres"
	"github.com/joho/godotenv"
	"go.mau.fi/whatsmeow"
	waCompanionReg "go.mau.fi/whatsmeow/binary/proto"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
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

	// Log level: APP_LOG=debug for verbose pairing-trace output,
	// APP_LOG=info (default) for normal operation, APP_LOG=warn
	// for quiet prod runs. The whatsmeow library's internal logger
	// is wired to slog at the same level (see below).
	slogLevel := slog.LevelInfo
	switch strings.ToLower(os.Getenv("APP_LOG")) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel})
	logger := slog.New(handler)
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
	// as Open returns. We pass a slog adapter so whatsmeow's internal
	// logs surface in our terminal at the same level as the rest of
	// the service (set APP_LOG=debug to see the full pairing trace).
	waLogger := waLogAdapter{logger: logger}
	mgr, err := instance.NewManager(db, store.SQLDriverName(cfg.DBDriver), waLogger)
	if err != nil {
		slog.Error("init instance manager", "err", err)
		os.Exit(1)
	}
	// Make sure whatsmeow reports a believable client identity
	// (version + platform + history sync policy) to the server.
	// The library default is "0.1.0", PlatformType=UNKNOWN, and
	// full history sync — that's what was making the phone's
	// "Linked Devices" page show "Other device" and the new
	// device download every recent message. See the long comment
	// above setClientIdentity for the rationale on each knob.
	setClientIdentity(logger)
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
		// Echo every event to the terminal so the operator can see
		// the full pairing timeline in the logs (the whatsmeow
		// library logger is set to slog.Debug below, but the
		// event stream is the source of truth for "what did
		// WhatsApp say about this device").
		slog.Debug("whatsmeow event", "id", instanceID, "type", eventTypeName(evt), "evt", evt)
		// Persist status transitions to the DB so GET /admin/api/instances
		// and the manager UI see the same status the whatsmeow client has.
		switch e := evt.(type) {
		case *events.Connected:
			now := time.Now().UTC()
			_ = instanceStore.SetStatus(instanceID, instance.StatusConnected, &now, &now)
			// Persist the device identity (JID / LID / phone)
			// so the manager UI can show them. The whatsmeow
			// Connected event itself is empty — we have to look
			// at the client to get the identity. For an
			// already-paired device, the identity is the same
			// on every reconnect; for a freshly-paired device
			// this is the first time we know the phone number.
			if cli := mgr.Get(instanceID); cli != nil && cli.Store != nil && cli.Store.ID != nil {
				jid := cli.Store.ID.String()
				lid := ""
				// LID is a value type, not a pointer —
				// check User instead of nil.
				if cli.Store.LID.User != "" {
					lid = cli.Store.LID.String()
				}
				// Phone = JID user portion (strip the
				// ":deviceid" suffix). For
				// 5551933811858:8@s.whatsapp.net this
				// gives 5551933811858.
				phone := ""
				if u := cli.Store.ID.User; u != "" {
					if i := strings.IndexByte(u, ':'); i >= 0 {
						phone = u[:i]
					} else {
						phone = u
					}
				}
				if err := instanceStore.SetIdentity(instanceID, jid, lid, phone); err != nil {
					slog.Warn("set identity", "id", instanceID, "err", err)
				} else {
					slog.Info("device identity persisted",
						"id", instanceID,
						"jid", jid,
						"lid", lid,
						"phone", phone)
				}
			}
		case *events.Disconnected:
			// If the disconnect was operator-driven, the handler has
			// already set the status; this is the network-blip case.
			if !mgr.IsExpectedDisconnect(instanceID) {
				_ = instanceStore.SetStatus(instanceID, instance.StatusDisconnected, nil, nil)
			}
			slog.Info("instance disconnected", "id", instanceID, "reason", disconnectReason(e))
		case *events.LoggedOut:
			// Server revoked the session (phone unlinked the
			// device, or kicked us for some other reason).
			// Evict the in-memory client (it's referencing a
			// deleted whatsmeow_device) and flip the instance
			// back to "pairing" so the next page load renders a
			// fresh QR. Without this, the client sticks around
			// as a zombie: auto-reconnect loops on "invalid use
			// of deleted device", GetLatestQR fails, the UI
			// keeps showing "connected" because the cached
			// IsLoggedIn() is stale. See Manager.LogoutAndReset.
			slog.Warn("instance logged out", "id", instanceID, "reason", e.Reason.String())
			if err := mgr.LogoutAndReset(instanceID); err != nil {
				slog.Warn("logout-and-reset", "id", instanceID, "err", err)
			}
		case *events.ConnectFailure:
			// WhatsApp's server sent a <failure> node to the
			// connect handshake. This is where "Cant link new
			// devices right now" or "Too many devices" errors
			// surface. The Reason is a typed enum (400 generic,
			// 401 logged out, 402 temp banned, 403 main-device-gone,
			// etc.); Message is the human-readable string.
			slog.Warn("connect failure",
				"id", instanceID,
				"reason_code", e.Reason.NumberString(),
				"reason", e.Reason.String(),
				"message", e.Message)
		case *events.PairError:
			// Server said pair-success but finishing the pairing
			// locally failed. Rare, but log it.
			slog.Warn("pair error", "id", instanceID, "err", e.Error)
		case *events.PairPasskeyError:
			slog.Warn("pair passkey error", "id", instanceID, "err", e.Error)
		}
		// Push events.QR into the per-instance QRState buffer so
		// HTTP handlers can serve the latest code without ever
		// calling whatsmeow's GetQRChannel (which has a known
		// side effect of Disconnect'ing the client the moment the
		// consumer stops reading — see qr.go and manager.go for
		// the full story).
		if e, ok := evt.(*events.QR); ok {
			if state := mgr.QRState(instanceID); state != nil {
				state.Set(e.Codes)
			}
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
	// F-04 / US-002: per-process audit bus. The logger publishes every
	// Log call to the bus; the SSE handler subscribes and pipes entries
	// out as `audit_entry` SSE events. One bus instance is shared so
	// the bus reference the logger holds is the same one the SSE
	// handler reads from.
	auditBus := handlers.NewAuditBus()
	audit := handlers.NewAuditLogger(db, auditBus)
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
		DB:              db,
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
	// Form submit for the new-instance page (was missing — the form
	// posted to /admin/instances/new but only the GET handler was
	// registered, so the POST fell through to a form-dispatcher and
	// bounced back with "Unknown action:"). Mirrors the same
	// pattern as the lifecycle / api-key / delete handlers:
	// form posts → handler runs → 302 to the next page with
	// msg + msg_class in the query string.
	adminUI.POST("/instances/new", handlers.AdminNewSubmit(instanceStore, mgr))
	adminUI.GET("/instances/:id", handlers.AdminDetailPage(db, mgr))
	// Form-based dispatcher for the lifecycle buttons in the
	// manager detail page (Connect/Disconnect/Reconnect). Reads
	// the `action` form value and routes to the right internal
	// method, then redirects back to the detail page.
	adminUI.POST("/instances/:id", handlers.LifecycleActionHandler(lifecycleDeps))
	// Form-based dispatcher for the api-key buttons (Rotate /
	// Delete). The HTML forms submit to /admin/instances/{id}/...
	// (no /api/ segment, no DELETE verb) so we need a shim.
	// (The old `reveal-key` form action was removed when the
	// manager-password / Re-fetch-from-DB UI was dropped — the
	// JSON API at POST /admin/api/instances/{id}/api-key/reveal
	// still handles programmatic reveals.)
	adminUI.POST("/instances/:id/api-key/rotate", handlers.APIKeyFormActionHandler(apiKeyDeps))
	adminUI.POST("/instances/:id/delete", handlers.APIKeyFormActionHandler(apiKeyDeps))
	// Webhook form-dispatcher: HTML forms can only POST, but the
	// JSON API uses PUT /admin/api/instances/:id/webhook. This
	// wrapper lets the detail page's form work without JS.
	adminUI.POST("/instances/:id/webhook", handlers.WebhookFormActionHandler(db, mgr, instanceStore))
	adminUI.GET("/audit", handlers.AdminAuditPage(db))

	// Swagger UI + raw OpenAPI spec (US-017 + US-018, gated by US-037).
	// Mounted on the root router (not under /admin/*) so the URL
	// stays /swagger and /swagger/openapi.yaml — the chromeHeader nav
	// link points at /swagger, and external API tools may already
	// know these endpoints. The per-route SessionMiddleware enforces
	// a valid manager session: an unauthenticated browser gets
	// 302 → /admin/login; an API client gets 401 JSON. Previously
	// these routes were on the root router WITHOUT any auth,
	// exposing the full API contract to anyone.
	r.GET("/swagger", auth.SessionMiddleware(sessions), handlers.SwaggerUIHandler())
	r.GET("/swagger/openapi.yaml", auth.SessionMiddleware(sessions), handlers.OpenAPISpecHandler())

	// Manager-authenticated JSON API.
	adminAPI := r.Group("/admin/api", auth.SessionMiddleware(sessions))
	adminAPI.POST("/instances", handlers.CreateInstanceHandler(instanceStore))
	adminAPI.GET("/instances", handlers.ListInstancesHandler(instanceStore))
	adminAPI.GET("/instances/:id", handlers.GetInstanceHandler(instanceStore))
	adminAPI.GET("/instances/:id/qr", handlers.InstanceQRHandler(mgr))
	adminAPI.GET("/instances/:id/status", handlers.InstanceStatusHandler(mgr))
	adminAPI.GET("/instances/:id/logs", handlers.ListInstanceLogsHandler(instanceStore))
	// Live event stream (F-02/US-039 + F-04). Browser opens an
	// EventSource here; the server pushes `status`, `qr_update`, and
	// `audit_entry` events as whatsmeow state changes and audit
	// entries are written. Stays under SessionMiddleware so the
	// event stream isn't publicly readable. The audit bus is
	// per-process; passing it here lets the handler subscribe to
	// live entries written by AuditLoggerImpl.Log.
	adminAPI.GET("/events", handlers.EventsHandler(mgr, auditBus))
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
	case *events.ConnectFailure:
		// "Cant link new devices right now", "Too many devices",
		// "Logged out", etc. all surface here with a typed reason
		// code and a human-readable message. We record the data
		// verbatim so the operator can correlate with whatever
		// their phone actually showed.
		level = instance.LogLevelWarn
		category = instance.LogCategoryConnect
		message = "connect failure from server"
		data = map[string]any{
			"reason_code": e.Reason.NumberString(),
			"reason":      e.Reason.String(),
			"message":     e.Message,
		}
	case *events.PairError:
		level = instance.LogLevelWarn
		category = instance.LogCategoryConnect
		message = "pair error"
		data = map[string]any{"error": e.Error.Error()}
	case *events.PairPasskeyError:
		level = instance.LogLevelWarn
		category = instance.LogCategoryConnect
		message = "pair passkey error"
		data = map[string]any{
			"error":        e.Error.Error(),
			"continuation": e.Continuation,
		}
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

// eventTypeName returns a short human-readable name for a whatsmeow
// event type. Used in the terminal log line ("whatsmeow event ...")
// so the operator can scan a stream of events without expanding the
// full event payload each time.
func eventTypeName(evt interface{}) string {
	switch evt.(type) {
	case *events.Connected:
		return "Connected"
	case *events.Disconnected:
		return "Disconnected"
	case *events.LoggedOut:
		return "LoggedOut"
	case *events.ConnectFailure:
		return "ConnectFailure"
	case *events.PairSuccess:
		return "PairSuccess"
	case *events.PairError:
		return "PairError"
	case *events.PairPasskeyRequest:
		return "PairPasskeyRequest"
	case *events.PairPasskeyConfirmation:
		return "PairPasskeyConfirmation"
	case *events.PairPasskeyError:
		return "PairPasskeyError"
	case *events.QR:
		return "QR"
	case *events.Message:
		return "Message"
	case *events.Receipt:
		return "Receipt"
	case *events.GroupInfo:
		return "GroupInfo"
	case *events.Contact:
		return "Contact"
	case *events.Presence:
		return "Presence"
	case *events.ManualLoginReconnect:
		return "ManualLoginReconnect"
	default:
		return fmt.Sprintf("%T", evt)
	}
}

// disconnectReason returns a short human-readable tag for a
// Disconnected event. The event is currently an empty struct
// (just "the websocket was closed") — there's no reason code
// to surface. The handler that called Disconnect() (the operator
// path) is the one that knows if it was operator-driven; the
// subscriber just notes that a disconnect happened.
func disconnectReason(e *events.Disconnected) string {
	if e == nil {
		return ""
	}
	return "websocket closed by server"
}

// waLogAdapter is a thin adapter that pipes whatsmeow's internal
// waLog.Logger calls into our slog handler. Lets us see the library's
// "Sending QR code request", "Got QR event", "Connection closed by
// server" type messages in the same JSON log stream as the rest of
// the service. APP_LOG=debug enables the chatty ones.
type waLogAdapter struct {
	logger *slog.Logger
	module string
}

func (w waLogAdapter) Debugf(msg string, args ...any) {
	w.logger.Debug("whatsmeow."+w.module, "msg", fmt.Sprintf(msg, args...))
}
func (w waLogAdapter) Infof(msg string, args ...any) {
	w.logger.Info("whatsmeow."+w.module, "msg", fmt.Sprintf(msg, args...))
}
func (w waLogAdapter) Warnf(msg string, args ...any) {
	w.logger.Warn("whatsmeow."+w.module, "msg", fmt.Sprintf(msg, args...))
}
func (w waLogAdapter) Errorf(msg string, args ...any) {
	w.logger.Error("whatsmeow."+w.module, "msg", fmt.Sprintf(msg, args...))
}
func (w waLogAdapter) Sub(module string) waLog.Logger {
	return waLogAdapter{logger: w.logger, module: w.module + "." + module}
}

// setClientIdentity makes sure whatsmeow reports a believable
// client identity to the server during pairing. Three things go
// into the DeviceProps protobuf struct that gets baked into the
// pairing handshake:
//
//  1. Version. The library default is Version_Primary=0 ("0.1.0")
//     — that's what our debug logs were showing and is almost
//     certainly part of why the phone's local cache rejects our
//     pairing as "suspicious ancient client" before it even
//     forwards the request to WhatsApp's servers.
//
//  2. PlatformType. Default UNKNOWN — the phone shows the linked
//     device as "Other device" instead of "Chrome". We set it to
//     CHROME so the phone shows it as a Chrome Web client, which
//     is what we're actually impersonating.
//
//  3. HistorySyncConfig. The defaults are
//     FullSyncDaysLimit=nil/0, FullSyncSizeMbLimit=nil/0,
//     InlineInitialPayloadInE2EeMsg=true, ThumbnailSyncDaysLimit=60,
//     SupportCallLogHistory=true, etc. — which means "send me
//     everything you've got". For a programmatic API we don't
//     want any of that. Setting all the limits to 0 and the
//     booleans to false tells the server "skip the history sync
//     entirely". The new device starts with zero message history.
//
// We try the live version fetch first (matches the evolution-go
// reference implementation) and fall back to a hardcoded recent-ish
// value if the network call fails. The hardcoded value is in the
// 2.3000.x range from mid-2026 — recent enough to look like a
// real client to the phone's local cache, old enough to be safe
// even if the actual current version has changed.
//
// IMPORTANT: there are TWO version globals in the whatsmeow store.
// `store.SetWAVersion(v)` updates the version used for the
// ClientPayload (sent on every message). `store.DeviceProps.Version`
// is the version baked into the pairing DeviceProps. Both need to
// be updated or the QR still reports "0.1.0" in the device props
// the server sees during handshake. We update both.
//
// Caveat for already-paired devices: the PlatformType and
// HistorySyncConfig are baked into the device identity at the
// moment of pairing. Changing these globals at boot only affects
// NEW devices — to see "Chrome" and zero history sync on an
// already-paired instance, the user must unlink on the phone and
// re-pair. (The server caches the device identity it first saw.)
//
// We also cache the fetched version (15min TTL) to avoid hammering
// web.whatsapp.com on every instance start in a multi-instance
// setup. Note: whatsmeow's own GetLatestVersion already sets a
// proper Chrome User-Agent plus Sec-Fetch-* and Accept-Language
// headers (see vendor update.go:37-43) — so unlike the
// evolution-go reference, we don't have the "Go default UA" bug.
const clientVersionCacheTTL = 15 * time.Minute

var (
	cachedClientVersion   waStore.WAVersionContainer
	cachedClientVersionAt time.Time
	cachedClientVersionMu sync.Mutex
)

func setClientIdentity(logger *slog.Logger) {
	// 1. Identity: PlatformType=CHROME (default UNKNOWN makes the
	// phone show "Other device") + HistorySyncConfig all zeroed
	// out (default is "send me everything") + Os="slimwhats" (default
	// "whatsmeow" shows up in the phone's Linked Devices list as
	// "Google Chrome (whatsmeow)"). All three are baked into the
	// pairing handshake — only affect future pairings.
	waStore.DeviceProps.Os = proto.String("slimwhats")
	waStore.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
	waStore.DeviceProps.HistorySyncConfig = &waCompanionReg.DeviceProps_HistorySyncConfig{
		// All size/day limits to 0 = "don't sync".
		FullSyncDaysLimit:             proto.Uint32(0),
		FullSyncSizeMbLimit:           proto.Uint32(0),
		StorageQuotaMb:                proto.Uint32(0),
		RecentSyncDaysLimit:           proto.Uint32(0),
		ThumbnailSyncDaysLimit:        proto.Uint32(0),
		InitialSyncMaxMessagesPerChat: proto.Uint32(0),
		// All booleans to false = "no support for these features".
		// (InlineInitialPayloadInE2EeMsg=true is the "first
		// connection comes with a fat history blob in the first
		// E2E message" path — definitely off.)
		InlineInitialPayloadInE2EeMsg:            proto.Bool(false),
		SupportCallLogHistory:                    proto.Bool(false),
		SupportBotUserAgentChatHistory:           proto.Bool(false),
		SupportCagReactionsAndPolls:              proto.Bool(false),
		SupportBizHostedMsg:                      proto.Bool(false),
		SupportRecentSyncChunkMessageCountTuning: proto.Bool(false),
		SupportHostedGroupMsg:                    proto.Bool(false),
		SupportFbidBotChatHistory:                proto.Bool(false),
		SupportAddOnHistorySyncMigration:         proto.Bool(false),
		SupportMessageAssociation:                proto.Bool(false),
		SupportGroupHistory:                      proto.Bool(false),
		SupportManusHistory:                      proto.Bool(false),
		SupportHatchHistory:                      proto.Bool(false),
	}
	logger.Info("client identity set",
		"os", "slimwhats",
		"platform", "chrome",
		"history_sync", "disabled",
	)

	// 2. Version. Check the cache first. A multi-instance setup
	// creates N instances at boot — we don't want N parallel
	// HTTP calls to web.whatsapp.com.
	cachedClientVersionMu.Lock()
	if !cachedClientVersionAt.IsZero() && time.Since(cachedClientVersionAt) < clientVersionCacheTTL {
		version := cachedClientVersion
		cachedClientVersionMu.Unlock()
		applyClientVersion(logger, version, "cache")
		return
	}
	cachedClientVersionMu.Unlock()

	// Hardcoded fallback. Matches the era of mid-2026 WhatsApp Web
	// releases. Update this periodically (every few months) or
	// better yet, let the live fetch win.
	const fallbackVersion = "2.3000.101880"
	var version waStore.WAVersionContainer
	if parsed, err := waStore.ParseVersion(fallbackVersion); err == nil {
		version = parsed
		logger.Info("whatsmeow client version set (hardcoded fallback)", "version", parsed.String())
	} else {
		logger.Warn("hardcoded fallback version unparseable; using library default", "err", err)
	}
	// Try the live fetch and override if it succeeds. 5s timeout
	// so a flaky network can't block boot. Pass nil to let
	// whatsmeow use its own tuned http.Client (which already
	// sends a proper Chrome User-Agent — see update.go:37).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if latest, err := whatsmeow.GetLatestVersion(ctx, nil); err == nil {
		version = *latest
		logger.Info("whatsmeow client version set (live fetch)", "version", latest.String())
		// Persist in the cache for the next instance.
		cachedClientVersionMu.Lock()
		cachedClientVersion = version
		cachedClientVersionAt = time.Now()
		cachedClientVersionMu.Unlock()
	} else {
		logger.Warn("could not fetch live client version from web.whatsapp.com; using fallback",
			"err", err, "fallback", fallbackVersion)
	}
	applyClientVersion(logger, version, "boot")
}

func applyClientVersion(logger *slog.Logger, version waStore.WAVersionContainer, source string) {
	// Apply to BOTH globals: the ClientPayload one (SetWAVersion) and
	// the DeviceProps one (the protobuf struct that gets sent in
	// the pairing handshake).
	waStore.SetWAVersion(version)
	waStore.DeviceProps.Version = &waCompanionReg.DeviceProps_AppVersion{
		Primary:   proto.Uint32(version[0]),
		Secondary: proto.Uint32(version[1]),
		Tertiary:  proto.Uint32(version[2]),
	}
	logger.Info("client version applied", "source", source, "version", version.String())
}
