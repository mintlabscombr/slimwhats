package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// CreateInstanceRequest is the JSON body for POST /admin/api/instances.
type CreateInstanceRequest struct {
	Name   string `json:"name" binding:"required"`
	APIKey string `json:"api_key,omitempty"`
}

// CreateInstanceResponse is the JSON body for the create response.
type CreateInstanceResponse struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	APIKey string             `json:"api_key"`
	Status instance.Status    `json:"status"`
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

// InstanceView is the read-side representation of an instance. It
// deliberately omits the API key hash. Masked last-4 is shown instead
// of the full key (US-031 reveal flow surfaces the full key when needed).
type InstanceView struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Status           instance.Status `json:"status"`
	Phone            string          `json:"phone,omitempty"`
	JID              string          `json:"jid,omitempty"`
	LID              string          `json:"lid,omitempty"`
	WebhookURL       string          `json:"webhook_url,omitempty"`
	WebhookConfigured bool           `json:"webhook_configured"`
	APIKeyMasked     string          `json:"api_key_masked"`
	APISetAt         string          `json:"api_key_set_at,omitempty"`
	ConnectedAt      string          `json:"connected_at,omitempty"`
	LastSeenAt       string          `json:"last_seen_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
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
		ID:        inst.ID,
		Name:      inst.Name,
		Status:    inst.Status,
		APIKeyMasked: maskAPIKey(inst.APIKeyHash),
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

// maskAPIKey returns a placeholder masked representation. The hash is
// bcrypt and we never have the plaintext after creation (except via
// US-031 reveal). For display we show the prefix and last 4 chars of
// the hash to keep the API surface stable.
func maskAPIKey(hash string) string {
	if len(hash) < 4 {
		return "sk_live_••••"
	}
	// We don't have the plaintext, so we show a fixed-shape placeholder
	// with the last 4 chars of the bcrypt hash. Operators use US-031 to
	// reveal the full plaintext.
	return "sk_live_••••••••" + hash[len(hash)-4:]
}
