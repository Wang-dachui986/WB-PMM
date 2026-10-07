package main

import (
	"os"
	"path/filepath"
	"testing"

	"workbuddy2api/internal/upstream"
)

func TestDesktopProxyConfigLoadsDedicatedField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"desktop":{"proxy_url":"http://127.0.0.1:9123"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Desktop.ProxyURL != "http://127.0.0.1:9123" {
		t.Fatal("dedicated desktop proxy was lost")
	}
	client, err := upstream.NewDesktop(cfg.Desktop.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP.CloseIdleConnections() // Construction only; no request reaches this port.
}
