package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
)

// statusResponse is returned by GetStatus and includes runtime status fields.
type statusResponse struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	BuildDate    string `json:"build_date"`
	Uptime       string `json:"uptime,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	WatcherState bool   `json:"watcher_state"`
}

// GetStatus reports runtime server status (uptime, build info, watcher state).
func (h *Handler) GetStatus(c *gin.Context) {
	h.statusMu.Lock()
	startedAt := h.startedAt
	watcherFn := h.watcherState
	h.statusMu.Unlock()

	resp := statusResponse{
		Status:    "ok",
		Version:   buildinfo.Version,
		Commit:    buildinfo.Commit,
		BuildDate: buildinfo.BuildDate,
	}
	if !startedAt.IsZero() {
		resp.StartedAt = startedAt.Format(time.RFC3339)
		resp.Uptime = time.Since(startedAt).Round(time.Second).String()
	}
	if watcherFn != nil {
		resp.WatcherState = watcherFn()
	}
	c.JSON(http.StatusOK, resp)
}
