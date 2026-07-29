package instance

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Manager owns the whatsmeow Container and one *whatsmeow.Client per
// instance. Threadsafe.
type Manager struct {
	DB        *sql.DB
	Container *sqlstore.Container
	Driver    string

	mu      sync.RWMutex
	clients map[string]*managedClient
}

// managedClient bundles the whatsmeow client with its on-disk device
// record.
type managedClient struct {
	instance *Instance
	device   *store.Device
	client   *whatsmeow.Client
}

// NewManager creates the whatsmeow Container over the given DB and
// initializes the client map. Call StartAll to actually create the
// whatsmeow Clients.
func NewManager(db *sql.DB, driver string) (*Manager, error) {
	container := sqlstore.NewWithDB(db, driver, waLog.Noop)
	return &Manager{
		DB:        db,
		Container: container,
		Driver:    driver,
		clients:   make(map[string]*managedClient),
	}, nil
}

// StartAll iterates the `instances` table, loads the whatsmeow Device
// for each (or creates one for never-paired instances), and starts a
// goroutine that owns the client. Already-paired clients get Connect()
// called immediately with auto-reconnect on.
func (m *Manager) StartAll(ctx context.Context) error {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, name FROM instances`)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.Start(ctx, id); err != nil {
			slog.Warn("instance start failed", "id", id, "err", err)
			// don't fail the whole boot for one bad instance
		}
	}
	return nil
}

// Start loads the instance, ensures a Device exists, creates the whatsmeow
// client, and registers it in the manager. For already-paired instances
// it calls Connect() with auto-reconnect on.
func (m *Manager) Start(ctx context.Context, instanceID string) error {
	inst, err := m.loadInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("instance not found: %s", instanceID)
	}

	// Get the JID we should use for the device. If the instance is
	// already paired, the JID is stored; if not, we mint a fresh
	// device with an empty JID (whatsmeow will fill it in on first pair).
	jid := ""
	if inst.JID.Valid {
		jid = inst.JID.String
	}

	var device *store.Device
	if jid == "" {
		// New device, never paired.
		device = m.Container.NewDevice()
	} else {
		device, err = m.Container.GetDevice(ctx, types.JID{User: jid, Server: "s.whatsapp.net"})
		if err != nil {
			return fmt.Errorf("get device for %s: %w", jid, err)
		}
	}

	client := whatsmeow.NewClient(device, nil)
	client.AutoTrustIdentity = true
	client.EnableAutoReconnect = true

	mc := &managedClient{instance: inst, device: device, client: client}
	m.mu.Lock()
	m.clients[instanceID] = mc
	m.mu.Unlock()

	// If the device has been paired (JID + identity keys present),
	// connect immediately. NewDevice() never has a JID until paired.
	if device.ID != nil {
		go func() {
			if err := client.Connect(); err != nil {
				slog.Warn("client connect failed", "id", instanceID, "err", err)
			}
		}()
	}
	return nil
}

// loadInstance fetches a single instance row by id.
func (m *Manager) loadInstance(ctx context.Context, id string) (*Instance, error) {
	row := m.DB.QueryRowContext(ctx, `
		SELECT id, name, api_key_hash, webhook_url, status, phone, jid, lid,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances WHERE id = ?`, id)
	return scanInstance(row)
}

// Get returns the whatsmeow Client for an instance, or nil if not loaded.
func (m *Manager) Get(instanceID string) *whatsmeow.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mc, ok := m.clients[instanceID]; ok {
		return mc.client
	}
	return nil
}

// GetByAPIKey looks up an instance by its plaintext API key, returning
// the instance and its whatsmeow client. Returns (nil, nil) on miss.
func (m *Manager) GetByAPIKey(ctx context.Context, plaintext string) (*Instance, *whatsmeow.Client, error) {
	insts, err := m.allInstances(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, inst := range insts {
		if VerifyAPIKey(inst.APIKeyHash, plaintext) {
			return inst, m.Get(inst.ID), nil
		}
	}
	return nil, nil, nil
}

// LookupByID returns the instance row + client by id, or (nil, nil) if
// the instance is unknown.
func (m *Manager) LookupByID(ctx context.Context, id string) (*Instance, *whatsmeow.Client, error) {
	inst, err := m.loadInstance(ctx, id)
	if err != nil || inst == nil {
		return nil, nil, err
	}
	return inst, m.Get(id), nil
}

func (m *Manager) allInstances(ctx context.Context) ([]*Instance, error) {
	rows, err := m.DB.QueryContext(ctx, `
		SELECT id, name, api_key_hash, webhook_url, status, phone, jid, lid,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		inst, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// StopAll disconnects all clients.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, mc := range m.clients {
		mc.client.Disconnect()
		slog.Info("instance client disconnected", "id", id)
	}
}

// All returns a snapshot of currently-managed instance IDs.
func (m *Manager) All() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.clients))
	for id := range m.clients {
		out = append(out, id)
	}
	return out
}
