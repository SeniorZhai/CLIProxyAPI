package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
)

func TestAdminRevocationAgainstPendingWatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	s := newAdminTestServer(t, path)
	session := s.loginAdminTest(t)
	response := s.adminTestRequest("POST", "/v8/management/client-keys", `{}`, session, "")
	var key struct{ ID, Key string }
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &key) != nil {
		t.Fatal("create key failed")
	}
	loaded, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	w, err := watcher.NewWatcher(path, s.getConfig().AuthDir, func(cfg *config.Config) {
		if first.CompareAndSwap(false, true) {
			close(loaded)
			<-resume
		}
		s.UpdateClientsContext(context.Background(), cfg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	w.SetConfig(s.getConfig())
	go func() { w.ReloadConfigIfChanged(); close(done) }()
	<-loaded
	s.mgmt.SetConfigReloadHook(func(context.Context, *config.Config) {
		close(resume)
		<-done
		w.ReloadConfigIfChanged()
	})
	response = s.adminTestRequest("DELETE", "/v8/management/client-keys/"+key.ID, "", session, "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete status %d", response.Code)
	}
	persisted, err := config.LoadConfig(path)
	if err != nil || len(persisted.APIKeys) != 0 {
		t.Fatal("disk should contain no keys")
	}
	response = s.adminTestRequest("GET", "/v1/models", "", adminTestSession{}, key.Key)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key restored by pending watcher: status=%d, disk key count=%d", response.Code, len(persisted.APIKeys))
	}
}
