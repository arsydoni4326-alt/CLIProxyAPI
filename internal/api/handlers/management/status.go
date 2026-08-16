package management

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
)

// blockedIPStatus describes a currently-blocked client IP and its remaining ban time.
type blockedIPStatus struct {
	IP        string `json:"ip"`
	Remaining string `json:"remaining"` // e.g. "29m59s"
}

// statusResponse is returned by GetStatus and includes runtime status fields.
type statusResponse struct {
	Status       string            `json:"status"`
	Version      string            `json:"version"`
	Commit       string            `json:"commit"`
	BuildDate    string            `json:"build_date"`
	Uptime       string            `json:"uptime,omitempty"`
	StartedAt    string            `json:"started_at,omitempty"`
	WatcherState bool              `json:"watcher_state"`
	BlockedIPs   []blockedIPStatus `json:"blocked_ips,omitempty"`
}

// blockedIPSnapshot returns the currently blocked client IPs with remaining ban
// time, sorted by IP for stable output. Expired entries are excluded and are
// left for the next authentication attempt or cleanup tick to reset.
func (h *Handler) blockedIPSnapshot() []blockedIPStatus {
	if h == nil {
		return nil
	}
	now := time.Now()
	var out []blockedIPStatus
	h.attemptsMu.Lock()
	for ip, ai := range h.failedAttempts {
		if ai == nil || ai.blockedUntil.IsZero() || !now.Before(ai.blockedUntil) {
			continue
		}
		out = append(out, blockedIPStatus{
			IP:        ip,
			Remaining: ai.blockedUntil.Sub(now).Round(time.Second).String(),
		})
	}
	h.attemptsMu.Unlock()
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
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
	resp.BlockedIPs = h.blockedIPSnapshot()
	c.JSON(http.StatusOK, resp)
}
