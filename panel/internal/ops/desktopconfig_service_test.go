package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/config"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/upstream"
)

func newDesktopConfigService(t *testing.T) *Service {
	t.Helper()
	files := newDesktopConfigFixture(t, desktopConfigFixture)
	root := filepath.Dir(files.path)
	authDir := filepath.Join(root, "auths")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := authstore.New(authDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.UpstreamConfigFile, cfg.UpstreamAuthDir, cfg.BackupDir, cfg.PricingFile = files.path, authDir, files.backupDir, ""
	s := New(cfg, store, gateway.New("http://127.0.0.1:1", func() string { return "STALE_FAKE_KEY" }, time.Second), upstream.New(time.Second))
	if err := s.EnableDesktopMaintenance("instance"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDesktopConfig(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDesktopConfigServiceMaintainsMutualExclusion(t *testing.T) {
	s := newDesktopConfigService(t)
	view, err := s.ReadDesktopConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	permit := authstore.MaintenancePermit{InstanceID: "instance", Generation: 1, Action: authstore.MaintainImport, UIDs: []string{"account-one"}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.GrantMaintenance(permit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":9000}`), false); !errors.Is(err, ErrDesktopConfigBusy) {
		t.Fatal("live idle permit did not block config", err)
	}
	ctx, done, err := s.beginMaintenance(s.MaintenanceContext(context.Background(), "instance", 1), authstore.MaintainImport, []string{"account-one"}, false)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RevokeMaintenance(cancelled, "instance", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("revoke should stay draining", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("maintenance worker was not cancelled")
	}
	if _, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":9000}`), false); !errors.Is(err, ErrDesktopConfigBusy) {
		t.Fatal("draining worker did not block config", err)
	}
	done()
	if err := s.RevokeMaintenance(context.Background(), "instance", 1); err != nil {
		t.Fatal(err)
	}
	tx, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":9000}`), false)
	if err != nil {
		t.Fatal(err)
	}
	permit.Generation = 2
	if err := s.GrantMaintenance(permit); !errors.Is(err, authstore.ErrMaintenanceRequired) {
		t.Fatal("prepared config allowed maintenance", err)
	}
	if err := s.FinishDesktopConfig("wrong"); !errors.Is(err, ErrDesktopConfigTransaction) {
		t.Fatal("wrong transaction ended config")
	}
	if _, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":9001}`), false); !errors.Is(err, ErrDesktopConfigBusy) {
		t.Fatal("wrong finish released active config")
	}
	if err := s.FinishDesktopConfig(tx.BackupID); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantMaintenance(permit); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeMaintenance(context.Background(), "instance", 2); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopConfigServicePublishesConnectionAndRestores(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		fmt.Fprint(w, `{"accounts":[]}`)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	s := newDesktopConfigService(t)
	old, err := os.ReadFile(s.desktopConfig.path)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := s.ReadDesktopConfig(context.Background())
	tx, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":`+port+`}`), false)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := s.CommitDesktopConfig(context.Background(), tx.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if s.gw.BaseURL() != server.URL {
		t.Fatal("commit kept old connection")
	}
	if _, err := s.gw.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "Bearer FAKE_CONFIG_KEY_DO_NOT_EXPORT" {
		t.Fatal("commit retained stale API-key callback")
	}
	if _, err := s.CommitDesktopConfig(context.Background(), tx.BackupID); !errors.Is(err, ErrDesktopConfigTransaction) {
		t.Fatal("replayed commit accepted")
	}
	if _, err := s.RestoreDesktopConfig(context.Background(), tx.BackupID, committed.Revision); err != nil {
		t.Fatal(err)
	}
	if s.gw.BaseURL() != "http://127.0.0.1:7863" {
		t.Fatal("restore kept candidate URL")
	}
	if raw, err := os.ReadFile(s.desktopConfig.path); err != nil || string(raw) != string(old) {
		t.Fatal("service rollback was not exact")
	}
	if s.desktopConfigTxn != nil {
		t.Fatal("restore did not release service transaction")
	}
}

func TestDesktopAccountProjectionPreservesManualDisable(t *testing.T) {
	s := newDesktopConfigService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"accounts":[{"uid":"account-one","disabled":true,"manual_disabled":true,"manual_reason":"paused by owner"}]}`)
	}))
	defer server.Close()
	s.gw.SetConnection(server.URL, "synthetic")
	accounts, _, _, err := s.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].ManualDisabled || accounts[0].ManualReason != "paused by owner" {
		t.Fatal("manual disable missing from account projection")
	}
	raw, _ := json.Marshal(accounts)
	if !strings.Contains(string(raw), `"manual_disabled":true`) {
		t.Fatal("manual disable missing from JSON")
	}
}

func TestDesktopConfigServiceRotationGeneratesPrivateKey(t *testing.T) {
	s := newDesktopConfigService(t)
	view, _ := s.ReadDesktopConfig(context.Background())
	tx, err := s.PrepareDesktopConfig(context.Background(), view.Revision, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitDesktopConfig(context.Background(), tx.BackupID); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.desktopConfig.path)
	doc, _ := configObject(raw)
	var key string
	_ = json.Unmarshal(doc["api_key"], &key)
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 || key == "FAKE_CONFIG_KEY_DO_NOT_EXPORT" {
		t.Fatal("rotation did not generate a 32-byte encoded key")
	}
	if err := s.FinishDesktopConfig(tx.BackupID); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopConfigServiceFinishRetryPreservesNewTransaction(t *testing.T) {
	s := newDesktopConfigService(t)
	ctx := context.Background()
	view, _ := s.ReadDesktopConfig(ctx)
	for _, id := range []string{"", "unknown"} {
		if err := s.FinishDesktopConfig(id); !errors.Is(err, ErrDesktopConfigTransaction) {
			t.Fatal("uncompleted ID acknowledged", err)
		}
	}
	first, err := s.PrepareDesktopConfig(ctx, view.Revision, json.RawMessage(`{"port":9000}`), false)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := s.CommitDesktopConfig(ctx, first.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDesktopConfig(first.BackupID); err != nil {
		t.Fatal(err)
	}
	second, err := s.PrepareDesktopConfig(ctx, committed.Revision, json.RawMessage(`{"port":9001}`), false)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan error, 8)
	for range 8 {
		go func() { replies <- s.FinishDesktopConfig(first.BackupID) }()
	}
	for range 8 {
		if err := <-replies; err != nil {
			t.Fatal("exact finish retry rejected", err)
		}
	}
	if s.desktopConfigTxn == nil || s.desktopConfigTxn.prepared.BackupID != second.BackupID ||
		s.desktopConfig.active == nil || s.desktopConfig.active.BackupID != second.BackupID {
		t.Fatal("old finish released newer transaction")
	}
	permit := authstore.MaintenancePermit{InstanceID: "instance", Generation: 1, Action: authstore.MaintainImport, UIDs: []string{"fake-one"}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.GrantMaintenance(permit); !errors.Is(err, authstore.ErrMaintenanceRequired) {
		t.Fatal("retry unlocked maintenance", err)
	}
	for _, id := range []string{"", "unknown"} {
		if err := s.FinishDesktopConfig(id); !errors.Is(err, ErrDesktopConfigTransaction) {
			t.Fatal("invalid finish acknowledged", err)
		}
	}
	if err := s.FinishDesktopConfig(second.BackupID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDesktopConfig(first.BackupID); !errors.Is(err, ErrDesktopConfigTransaction) {
		t.Fatal("superseded completion retained", err)
	}
}

func TestDesktopConfigServiceFailedFinishIsNotAcknowledged(t *testing.T) {
	s := newDesktopConfigService(t)
	view, _ := s.ReadDesktopConfig(context.Background())
	tx, err := s.PrepareDesktopConfig(context.Background(), view.Revision, json.RawMessage(`{"port":9000}`), false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a store rejection; the controller must not record a completion.
	s.desktopConfig.mu.Lock()
	s.desktopConfig.active = nil
	s.desktopConfig.mu.Unlock()
	for range 2 {
		if err := s.FinishDesktopConfig(tx.BackupID); !errors.Is(err, ErrDesktopConfigTransaction) {
			t.Fatal("failed finish became successful ACK", err)
		}
	}
	if s.lastFinishedBackupID != "" || s.desktopConfigTxn == nil {
		t.Fatal("failure recorded or released controller transaction")
	}
}
