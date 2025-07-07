package internal

import (
	"database/sql"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// RegisterWebsocketRoutes adds the websocket endpoint to the Gin router.
func RegisterWebsocketRoutes(r *gin.Engine, db *sql.DB) {
	// /ws/updates - WebSocket endpoint for real-time updates
	r.GET("/ws/updates", func(c *gin.Context) {
		handlerWebsocketUpdates(c, db)
	})
}

// handlerWebsocketUpdates handles the websocket connection, polling for new updates and pushing them to the client.
func handlerWebsocketUpdates(c *gin.Context, db *sql.DB) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to upgrade to websocket: %v", err)
		return
	}
	defer ws.Close()

	// Optional: filter by task_id
	taskIDFilter := c.Query("task_id")
	var filterQuery string
	var filterArgs []interface{}
	lastID := 0
	if taskIDFilter != "" {
		filterQuery = `SELECT id, update_type, task_id, step_id, payload, created_at FROM websocket_updates WHERE id > $1 AND task_id = $2 ORDER BY id ASC`
		filterArgs = []interface{}{lastID, taskIDFilter}
	} else {
		filterQuery = `SELECT id, update_type, task_id, step_id, payload, created_at FROM websocket_updates WHERE id > $1 ORDER BY id ASC`
		filterArgs = []interface{}{lastID}
	}

	closeChan := make(chan struct{})
	go func() {
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				closeChan <- struct{}{}
				return
			}
		}
	}()

	for {
		select {
		case <-closeChan:
			return // Client disconnected
		case <-time.After(2 * time.Second):
		}

		rows, err := db.Query(filterQuery, filterArgs...)
		if err != nil {
			// Log error to tmp/api-errors.log
			logFile, ferr := os.OpenFile("tmp/api-errors.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if ferr == nil {
				logMsg := time.Now().Format(time.RFC3339) + " | DB Query Error: " + err.Error() + "\n"
				logFile.WriteString(logMsg)
				logFile.Close()
			}
			ws.WriteMessage(1, []byte(`{"error": "db query failed"}`))
			continue
		}
		for rows.Next() {
			var id int
			var updateType string
			var taskID, stepID sql.NullInt64
			var payload string
			var createdAt time.Time
			if err := rows.Scan(&id, &updateType, &taskID, &stepID, &payload, &createdAt); err != nil {
				continue
			}
			ws.WriteMessage(1, []byte(payload))
			lastID = id
			if taskIDFilter != "" {
				filterArgs[0] = lastID // update lastID for next poll
			}
		}
		rows.Close()
		if taskIDFilter == "" {
			filterArgs[0] = lastID // update lastID for next poll
		}
	}
}

// --- WebSocket upgrader (gorilla/websocket) ---
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins (public updates)
	},
}
