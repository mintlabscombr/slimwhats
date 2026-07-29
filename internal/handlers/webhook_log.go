package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// WebhookDelivery is the API view of one webhook_deliveries row.
type WebhookDelivery struct {
	ID             string `json:"id"`
	InstanceID     string `json:"instance_id"`
	EventType      string `json:"event_type"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	LastStatusCode *int   `json:"last_status_code,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ListWebhookDeliveriesHandler handles GET /admin/api/instances/{id}/webhook-deliveries.
// Returns the most recent deliveries for an instance, filterable by status.
func ListWebhookDeliveriesHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit <= 0 || limit > 500 {
			limit = 50
		}
		status := c.Query("status")
		args := []any{id, limit}
		query := `SELECT id, instance_id, event_type, status, attempts, last_status_code, last_error, created_at, updated_at
			FROM webhook_deliveries WHERE instance_id = ?`
		if status != "" {
			query += " AND status = ?"
			args = []any{id, status, limit}
		}
		query += " ORDER BY created_at DESC LIMIT ?"

		rows, err := db.Query(query, args...)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "list_failed",
				"message": err.Error(),
			})
			return
		}
		defer rows.Close()
		var out []WebhookDelivery
		for rows.Next() {
			var d WebhookDelivery
			var sc sql.NullInt64
			var le sql.NullString
			if err := rows.Scan(&d.ID, &d.InstanceID, &d.EventType, &d.Status, &d.Attempts, &sc, &le, &d.CreatedAt, &d.UpdatedAt); err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error":   "scan_failed",
					"message": err.Error(),
				})
				return
			}
			if sc.Valid {
				v := int(sc.Int64)
				d.LastStatusCode = &v
			}
			if le.Valid {
				d.LastError = le.String
			}
			out = append(out, d)
		}
		c.JSON(http.StatusOK, gin.H{"deliveries": out, "limit": limit})
	}
}
