package management

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// quotaStatusEntry represents the quota state for a specific auth and optional model.
type quotaStatusEntry struct {
	AuthID        string            `json:"auth_id"`
	Provider      string            `json:"provider"`
	Label         string            `json:"label,omitempty"`
	Model         string            `json:"model,omitempty"`
	Exceeded      bool              `json:"exceeded"`
	Reason        string            `json:"reason,omitempty"`
	NextRecoverAt string            `json:"next_recover_at,omitempty"`
	BackoffLevel  int               `json:"backoff_level,omitempty"`
	ObservedAt    string            `json:"observed_at,omitempty"`
	Signals       map[string]string `json:"signals,omitempty"`
}

// GetQuotaStatus returns the current quota signal state for all credentials and models.
func (h *Handler) GetQuotaStatus(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()

	if manager == nil {
		c.JSON(http.StatusOK, gin.H{"quota_status": []quotaStatusEntry{}})
		return
	}

	auths := manager.List()
	entries := make([]quotaStatusEntry, 0, len(auths))

	for _, auth := range auths {
		if !coreauth.ProviderSupportsQuotaObservation(auth.Provider) {
			continue
		}
		// Auth-level quota state
		if !auth.Quota.ObservedAt.IsZero() || len(auth.Quota.Signals) > 0 {
			entry := authQuotaEntry(auth)
			entries = append(entries, entry)
		}
		// Model-level quota state
		for modelName, modelState := range auth.ModelStates {
			if modelState == nil {
				continue
			}
			quota := modelState.Quota
			if quota.ObservedAt.IsZero() && len(quota.Signals) == 0 {
				continue
			}
			entry := modelQuotaEntry(auth, modelName, quota)
			entries = append(entries, entry)
		}
	}

	// Sort by auth_id then model (empty model first) for stable output.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].AuthID != entries[j].AuthID {
			return entries[i].AuthID < entries[j].AuthID
		}
		if entries[i].Model != entries[j].Model {
			// Empty model (auth-level) comes before non-empty models.
			if entries[i].Model == "" {
				return true
			}
			if entries[j].Model == "" {
				return false
			}
			return entries[i].Model < entries[j].Model
		}
		return false
	})

	c.JSON(http.StatusOK, gin.H{"quota_status": entries})
}

// authQuotaEntry builds a quota status entry for auth-level quota state.
func authQuotaEntry(auth *coreauth.Auth) quotaStatusEntry {
	quota := auth.Quota
	return quotaStatusEntry{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Label:         auth.Label,
		Exceeded:      quota.Exceeded,
		Reason:        quota.Reason,
		NextRecoverAt: formatTimePtr(quota.NextRecoverAt),
		BackoffLevel:  quota.BackoffLevel,
		ObservedAt:    formatTimePtr(quota.ObservedAt),
		Signals:       quota.Signals,
	}
}

// modelQuotaEntry builds a quota status entry for model-level quota state.
func modelQuotaEntry(auth *coreauth.Auth, modelName string, quota coreauth.QuotaState) quotaStatusEntry {
	return quotaStatusEntry{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Label:         auth.Label,
		Model:         modelName,
		Exceeded:      quota.Exceeded,
		Reason:        quota.Reason,
		NextRecoverAt: formatTimePtr(quota.NextRecoverAt),
		BackoffLevel:  quota.BackoffLevel,
		ObservedAt:    formatTimePtr(quota.ObservedAt),
		Signals:       quota.Signals,
	}
}

// formatTimePtr formats a time.Time as RFC3339 or empty string if zero.
func formatTimePtr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
