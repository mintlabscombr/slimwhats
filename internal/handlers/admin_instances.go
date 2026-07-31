package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/slimwhats/internal/instance"
)

// CreateInstanceRequest is the JSON body for POST /admin/api/instances.
type CreateInstanceRequest struct {
	Name   string `json:"name" binding:"required"`
	APIKey string `json:"api_key,omitempty"`
}

// CreateInstanceResponse is the JSON body for the create response.
type CreateInstanceResponse struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	APIKey string          `json:"api_key"`
	Status instance.Status `json:"status"`
}

// CreateInstanceHandler handles POST /admin/api/instances. It auto-
// generates an API key if the operator didn't supply one. The plaintext
// key is returned ONCE in the response and never again — US-031's
// reveal endpoint requires a manager-password re-entry.
func CreateInstanceHandler(store *instance.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req CreateInstanceRequest
		if err := c.ShouldBind(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": "name field is required",
			})
			return
		}

		inst, plaintext, err := store.Create(instance.CreateInput{
			Name:   req.Name,
			APIKey: req.APIKey,
		})
		if err != nil {
			if errors.Is(err, instance.ErrNameTaken) {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"error":   "name_taken",
					"message": "an instance with this name already exists",
				})
				return
			}
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "validation_failed",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, CreateInstanceResponse{
			ID:     inst.ID,
			Name:   inst.Name,
			APIKey: plaintext,
			Status: inst.Status,
		})
	}
}

// InstanceView is the read-side representation of an instance.
//
// Post 2026-07-29 (drop-bcrypt), the API key is stored in plaintext.
// The full key is surfaced in the `APIKey` field for the manager UI's
// show/hide widget; the JSON API still returns only `APIKeyMasked`
// (last-4) to keep the on-the-wire payload small. The detail page
// uses `APIKey` directly.
//
// Same deal for `WebhookSecret` (post 2026-07-29 drop-encryption):
// the plaintext lives in `WebhookSecret` for the manager UI's
// show/hide widget, and the JSON API omits it (`json:"-"`).
type InstanceView struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Status            instance.Status `json:"status"`
	Phone             string          `json:"phone,omitempty"`
	JID               string          `json:"jid,omitempty"`
	LID               string          `json:"lid,omitempty"`
	WebhookURL        string          `json:"webhook_url,omitempty"`
	WebhookConfigured bool            `json:"webhook_configured"`
	WebhookSecret     string          `json:"-"` // plaintext; HTML only
	APIKeyMasked      string          `json:"api_key_masked"`
	APIKey            string          `json:"-"` // plaintext; HTML only
	APISetAt          string          `json:"api_key_set_at,omitempty"`
	ConnectedAt       string          `json:"connected_at,omitempty"`
	LastSeenAt        string          `json:"last_seen_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`

	// UI-only: which lifecycle buttons should be enabled. Computed
	// from Status in getInstanceView. The JSON API exposes `status`
	// and lets the caller derive these on their own — the bools
	// here are a template convenience so the detail page doesn't
	// need to inline a switch on every button.
	CanConnect    bool `json:"-"`
	CanDisconnect bool `json:"-"`
	CanReconnect  bool `json:"-"`
}

// ListInstancesHandler handles GET /admin/api/instances — paginated list.
func ListInstancesHandler(store *instance.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
		if limit <= 0 || limit > 500 {
			limit = 50
		}
		if offset < 0 {
			offset = 0
		}
		insts, err := store.ListAll(limit, offset)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "list_failed",
				"message": err.Error(),
			})
			return
		}
		out := make([]InstanceView, 0, len(insts))
		for _, inst := range insts {
			out = append(out, toView(inst))
		}
		c.JSON(http.StatusOK, gin.H{"instances": out, "limit": limit, "offset": offset})
	}
}

// GetInstanceHandler handles GET /admin/api/instances/{id}.
func GetInstanceHandler(store *instance.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		inst, err := store.GetByID(id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "lookup_failed",
				"message": err.Error(),
			})
			return
		}
		if inst == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "not_found",
				"message": "no instance with that id",
			})
			return
		}
		c.JSON(http.StatusOK, toView(inst))
	}
}

func toView(inst *instance.Instance) InstanceView {
	v := InstanceView{
		ID:           inst.ID,
		Name:         inst.Name,
		Status:       inst.Status,
		APIKeyMasked: maskAPIKey(inst.APIKey),
		APIKey:       inst.APIKey,
	}
	if inst.Phone.Valid {
		v.Phone = inst.Phone.String
	}
	if inst.JID.Valid {
		v.JID = inst.JID.String
	}
	if inst.LID.Valid {
		v.LID = inst.LID.String
	}
	if inst.WebhookURL.Valid {
		v.WebhookURL = inst.WebhookURL.String
		v.WebhookConfigured = true
	}
	if inst.WebhookSecret.Valid {
		v.WebhookSecret = inst.WebhookSecret.String
	}
	if inst.APISetAt.Valid {
		v.APISetAt = inst.APISetAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if inst.ConnectedAt.Valid {
		v.ConnectedAt = inst.ConnectedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if inst.LastSeenAt.Valid {
		v.LastSeenAt = inst.LastSeenAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	v.CreatedAt = inst.CreatedAt
	v.UpdatedAt = inst.UpdatedAt
	return v
}

// maskAPIKey returns a placeholder masked representation of the API
// key. Post 2026-07-29 (drop-bcrypt) we DO have the plaintext, but
// the JSON API surface still returns the masked form to keep payloads
// small and to make it harder to accidentally log the key.
func maskAPIKey(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	if len(plaintext) < 4 {
		return plaintext
	}
	return "sk_live_••••••••" + plaintext[len(plaintext)-4:]
}
