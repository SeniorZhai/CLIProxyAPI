package cliproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/admin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestServiceRunAdminWaitsForInitialWatcher(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "legacy"
		if enabled {
			name = "admin"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("MANAGEMENT_PASSWORD", "")
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			cfg := &config.Config{Host: "127.0.0.1", Port: 0, AuthDir: filepath.Join(dir, "auths")}
			if errWrite := os.WriteFile(path, []byte("{}"), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if enabled {
				cfg.RemoteManagement.Admin = config.AdminConfig{Enabled: true, PublicURL: "https://proxy.example.com"}
				if _, errInitialize := admin.Initialize(cfg.RemoteManagement.Admin, path, "admin", "test-admin-password"); errInitialize != nil {
					t.Fatal(errInitialize)
				}
			}

			watcherStarted := make(chan struct{})
			releaseWatcher := make(chan struct{})
			httpStarted := make(chan struct{})
			watcherCompleted := false
			service, errBuild := NewBuilder().
				WithConfig(cfg).
				WithConfigPath(path).
				WithWatcherFactory(func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
					return &WatcherWrapper{start: func(context.Context) error {
						close(watcherStarted)
						<-releaseWatcher
						watcherCompleted = true
						return nil
					}}, nil
				}).
				WithHooks(Hooks{OnAfterStart: func(*Service) {
					if watcherCompleted != enabled {
						t.Errorf("watcher completed at HTTP startup = %t, want %t", watcherCompleted, enabled)
					}
					close(httpStarted)
				}}).
				Build()
			if errBuild != nil {
				t.Fatal(errBuild)
			}

			ctx, cancel := context.WithCancel(context.Background())
			runStopped := make(chan struct{})
			var runErr error
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseWatcher) }) }
			t.Cleanup(func() {
				release()
				cancel()
				select {
				case <-runStopped:
				case <-time.After(5 * time.Second):
					t.Error("service did not stop")
				}
			})
			go func() {
				runErr = service.Run(ctx)
				close(runStopped)
			}()
			select {
			case <-watcherStarted:
			case <-runStopped:
				t.Fatalf("service stopped before watcher startup: %v", runErr)
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not start")
			}

			if scheduled := service.serverErr != nil; scheduled == enabled {
				t.Errorf("HTTP startup scheduled while initial watcher is blocked = %t, want %t", scheduled, !enabled)
			}
			select {
			case <-httpStarted:
				if enabled {
					t.Error("administrator HTTP startup completed before the initial watcher")
				}
			default:
				if !enabled {
					t.Error("legacy HTTP startup no longer precedes the initial watcher")
				}
			}

			release()
			select {
			case <-httpStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP startup did not complete after the initial watcher")
			}
			cancel()
			select {
			case <-runStopped:
				if runErr != nil && !errors.Is(runErr, context.Canceled) {
					t.Fatalf("service returned an unexpected error: %v", runErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("service did not stop")
			}
		})
	}
}
