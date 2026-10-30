package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type clientKey struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func describeClientKey(key string) clientKey {
	hash := sha256.Sum256([]byte(key))
	return clientKey{ID: hex.EncodeToString(hash[:]), Key: key}
}

func (h *Handler) ListClientKeys(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]clientKey, 0, len(h.cfg.APIKeys))
	for _, key := range h.cfg.APIKeys {
		keys = append(keys, describeClientKey(key))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

func (h *Handler) CreateClientKey(c *gin.Context) {
	var random [32]byte
	if _, errRandom := rand.Read(random[:]); errRandom != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key_generation_failed"})
		return
	}
	key := "sk-cpa-" + base64.RawURLEncoding.EncodeToString(random[:])
	if !h.updateClientKeys(c, func(keys []string) ([]string, bool) { return append(keys, key), true }) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, describeClientKey(key))
}

func (h *Handler) DeleteClientKey(c *gin.Context) {
	if !h.updateClientKeys(c, func(keys []string) ([]string, bool) {
		index := slices.IndexFunc(keys, func(key string) bool { return describeClientKey(key).ID == c.Param("id") })
		if index == -1 {
			c.JSON(http.StatusNotFound, gin.H{"error": "key_not_found"})
			return nil, false
		}
		return slices.Delete(keys, index, index+1), true
	}) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) updateClientKeys(c *gin.Context, update func([]string) ([]string, bool)) bool {
	// Serialize key writes with pending reloads so an older snapshot cannot restore a revoked key.
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	keys, ok := update(slices.Clone(h.cfg.APIKeys))
	if !ok {
		h.mu.Unlock()
		return false
	}
	next := h.cfg.CloneForRuntime()
	next.APIKeys = keys
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, next, true); errSave != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed"})
		return false
	}
	if h.accessManager != nil {
		if _, errApply := access.ApplyAccessProviders(h.accessManager, nil, next); errApply != nil {
			h.mu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reload_failed"})
			return false
		}
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSaveLocked(context.WithoutCancel(c.Request.Context()), snapshot)
	return true
}
