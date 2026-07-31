// Package handlers — GET /admin/api/instances/{id}/logs (US-029).
//
// Returns a paginated, filterable stream of instance_logs rows. The
// table is populated by the event subscriber in main.go, which mirrors
// every whatsmeow event to a log row before forwarding it to the
// webhook dispatcher.
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/slimwhats/internal/instance"
)

// LogResponse is the JSON view of a single instance_logs row.
type LogResponse struct {
	ID         string          `json:"id"`
	InstanceID string          `json:"instance_id"`
	Timestamp  time.Time       `json:"timestamp"`
	Level      string          `json:"level"`
	Category   string          `json:"category"`
	Message    string          `json:"message"`
	Data       json.RawMessage `json:"data"`
}

// ListInstanceLogsHandler — GET /admin/api/instances/{id}/logs.
// Query parameters:
//   - level    (info|warn|error|debug)  optional
//   - category (connect|message|...)     optional
//   - since    (RFC3339 timestamp)       optional
//   - limit    (default 50, max 500)     optional
//   - offset   (default 0)               optional
func ListInstanceLogsHandler(store *instance.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		q := instance.LogQuery{InstanceID: id}

		q.Level = c.Query("level")
		q.Category = c.Query("category")
		if s := c.Query("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				q.Since = &t
			} else {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":   "invalid_since",
					"message": "since must be RFC3339 (e.g. 2026-07-29T12:00:00Z)",
				})
				return
			}
		}
		q.Limit, _ = strconv.Atoi(c.DefaultQuery("limit", "50"))
		q.Offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))

		entries, err := store.ListLogs(ctx, q)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "list_failed",
				"message": err.Error(),
			})
			return
		}
		out := make([]LogResponse, 0, len(entries))
		for _, e := range entries {
			out = append(out, LogResponse{
				ID:         e.ID,
				InstanceID: e.InstanceID,
				Timestamp:  e.Timestamp,
				Level:      string(e.Level),
				Category:   string(e.Category),
				Message:    e.Message,
				Data:       e.Data,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"instance_id": id,
			"limit":       q.Limit,
			"offset":      q.Offset,
			"entries":     out,
		})
	}
}
