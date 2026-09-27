package api

import (
	"encoding/base64"
	"encoding/json"
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
	"workbuddy2api-gui/internal/ops"
	"workbuddy2api-gui/internal/upstream"
)

type configHTTPFixture struct {
	server                *Server
	handler               http.Handler
	cookie, control, path string
}

func newConfigHTTPFixture(t *testing.T) *configHTTPFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(dir, "auths")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	schedule := map[string]bool{}
	for _, key := range []string{"checkin_enabled", "travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"} {
		schedule[key] = false
	}
	raw, _ := json.Marshal(map[string]any{"listen": "127.0.0.1:9000", "api_key": "CONFIG_TEST_INFERENCE_KEY", "auth_dir": authDir, "state_file": filepath.Join(dir, "state.json"), "schedule": schedule})
	path := filepath.Join(dir, "gateway-config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.UI.Username, cfg.UI.Password = "desktop", "CONFIG_TEST_PASSWORD"
	cfg.UpstreamAuthDir, cfg.UpstreamConfigFile, cfg.BackupDir, cfg.PricingFile = authDir, path, filepath.Join(dir, "backups"), ""
	store, err := authstore.New(authDir)
	if err != nil {
		t.Fatal(err)
	}
	svc := ops.New(cfg, store, gateway.New("http://127.0.0.1:1", func() string { return "STALE_KEY" }, time.Second), upstream.New(time.Second))
	srv := NewDesktopServer(cfg, svc, http.NotFoundHandler(), "test")
	control := strings.Repeat("a1", 32)
	if err := srv.ConfigureDesktopMaintenance("config-instance", control); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableDesktopConfig(); err != nil {
		t.Fatal(err)
	}
	cookie, ok, _ := srv.sessions.Create(cfg.UI.Username, cfg.UI.Password, cfg, "127.0.0.1")
	if !ok {
		t.Fatal("login")
	}
	return &configHTTPFixture{server: srv, handler: srv.Handler(), cookie: cookie, control: control, path: path}
}

func (f *configHTTPFixture) request(method, path, body, control string, cookie bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:9901"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:9901")
	if control != "" {
		r.Header.Set("X-WorkBuddy-Control-Key", control)
	}
	if cookie {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.cookie})
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func configControlBody(operation string, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["operation"], fields["instance_id"] = operation, "config-instance"
	raw, _ := json.Marshal(fields)
	return string(raw)
}

func configHTTPView(t *testing.T, f *configHTTPFixture) ops.DesktopConfigView {
	t.Helper()
	w := f.request("GET", "/api/config", "", "", true)
	if w.Code != 200 {
		t.Fatalf("read status=%d", w.Code)
	}
	for _, secret := range []string{"CONFIG_TEST_INFERENCE_KEY", f.control, f.cookie, filepath.Dir(f.path), "api_key", "auth_dir"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("configuration projection leaked protected data")
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("configuration projection can be cached")
	}
	var view ops.DesktopConfigView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestDesktopConfigHTTPBoundaries(t *testing.T) {
	f := newConfigHTTPFixture(t)
	before, _ := os.ReadFile(f.path)
	view := configHTTPView(t, f)
	valid := configControlBody("prepare", map[string]any{"revision": view.Revision, "patch": map[string]int{"port": 9001}})
	for _, tc := range []struct {
		method, path, body, key string
		cookie                  bool
		status                  int
	}{
		{"GET", "/api/config", "", "", false, 401},
		{"PUT", "/api/config", `{}`, "", true, 403},
		{"POST", "/api/config/reset", `{}`, "", true, 403},
		{"POST", "/api/desktop/config", valid, "", true, 401},
		{"POST", "/api/desktop/config", valid, "wrong", true, 401},
		{"POST", "/api/desktop/config", strings.Replace(valid, "config-instance", "another-instance", 1), f.control, false, 403},
		{"POST", "/api/desktop/config", `{"operation":"prepare","instance_id":"config-instance","unknown":true}`, f.control, false, 400},
		{"POST", "/api/desktop/config", `{"operation":"prepare","operation":"finish","instance_id":"config-instance"}`, f.control, false, 400},
		{"POST", "/api/desktop/config", configControlBody("prepare", map[string]any{"revision": view.Revision, "rotate_key": false}), f.control, false, 400},
		{"POST", "/api/desktop/config", configControlBody("prepare", map[string]any{"revision": view.Revision, "rotate_key": true, "patch": map[string]int{"port": 9001}}), f.control, false, 400},
		{"POST", "/api/desktop/config", configControlBody("prepare", map[string]any{"revision": view.Revision, "patch": map[string]string{"api_key": "injected"}}), f.control, false, 400},
	} {
		w := f.request(tc.method, tc.path, tc.body, tc.key, tc.cookie)
		if w.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	if after, _ := os.ReadFile(f.path); string(after) != string(before) {
		t.Fatal("rejected request changed config")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.path), "backups")); !os.IsNotExist(err) {
		t.Fatal("rejected request wrote a backup")
	}
}

type discardedConfigResponse struct{ *httptest.ResponseRecorder }

func (w discardedConfigResponse) Write(p []byte) (int, error) { return len(p), nil }

func TestDesktopConfigHTTPApplyLostResponseRestoreAndRotation(t *testing.T) {
	f := newConfigHTTPFixture(t)
	before, _ := os.ReadFile(f.path)
	view := configHTTPView(t, f)
	prepare := func(fields map[string]any) ops.PreparedDesktopConfig {
		w := f.request("POST", "/api/desktop/config", configControlBody("prepare", fields), f.control, false)
		if w.Code != 200 {
			t.Fatalf("prepare status=%d", w.Code)
		}
		var tx ops.PreparedDesktopConfig
		if err := json.Unmarshal(w.Body.Bytes(), &tx); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	tx := prepare(map[string]any{"revision": view.Revision, "patch": map[string]int{"port": 9001}})
	wrong := f.request("POST", "/api/desktop/config", configControlBody("finish", map[string]any{"backup_id": "wrong"}), f.control, false)
	if wrong.Code != 409 {
		t.Fatal("wrong finish accepted")
	}
	busy := f.request("POST", "/api/desktop/config", configControlBody("prepare", map[string]any{"revision": view.Revision, "patch": map[string]int{"port": 9002}}), f.control, false)
	if busy.Code != 409 || !strings.Contains(busy.Body.String(), "CONFIG_BUSY") {
		t.Fatal("active transaction not protected")
	}
	permit := map[string]any{"operation": "begin", "instance_id": "config-instance", "generation": 1, "action": "import", "uids": []string{"account-one"}, "expires_at": time.Now().Add(time.Minute)}
	payload, _ := json.Marshal(permit)
	if w := f.request("POST", "/api/desktop/maintenance", string(payload), f.control, false); w.Code != 409 {
		t.Fatal("maintenance overlapped config")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9901/api/desktop/config", strings.NewReader(configControlBody("commit", map[string]any{"backup_id": tx.BackupID})))
	r.Header.Set("Origin", "http://127.0.0.1:9901")
	r.Header.Set("X-WorkBuddy-Control-Key", f.control)
	lost := httptest.NewRecorder()
	f.handler.ServeHTTP(discardedConfigResponse{lost}, r)
	if lost.Body.Len() != 0 {
		t.Fatal("test did not discard commit response")
	}
	if got := configHTTPView(t, f); got.Revision != tx.CandidateRevision {
		t.Fatal("lost response was treated as a failed write")
	}
	if f.server.svc.Gateway().BaseURL() != "http://127.0.0.1:9001" {
		t.Fatal("committed connection not published")
	}
	if w := f.request("POST", "/api/desktop/config", configControlBody("commit", map[string]any{"backup_id": tx.BackupID}), f.control, false); w.Code != 409 {
		t.Fatal("replayed commit accepted")
	}
	if w := f.request("POST", "/api/desktop/config", configControlBody("restore", map[string]any{"backup_id": tx.BackupID, "current_revision": tx.CandidateRevision}), f.control, false); w.Code != 200 {
		t.Fatalf("rollback status=%d", w.Code)
	}
	if after, _ := os.ReadFile(f.path); string(after) != string(before) {
		t.Fatal("HTTP rollback was not byte exact")
	}
	if f.server.svc.Gateway().BaseURL() != "http://127.0.0.1:9000" {
		t.Fatal("rollback connection not restored")
	}
	tx = prepare(map[string]any{"revision": view.Revision, "rotate_key": true})
	w := f.request("POST", "/api/desktop/config", configControlBody("commit", map[string]any{"backup_id": tx.BackupID}), f.control, false)
	if w.Code != 200 {
		t.Fatal("rotation commit failed")
	}
	var doc map[string]json.RawMessage
	raw, _ := os.ReadFile(f.path)
	_ = json.Unmarshal(raw, &doc)
	var key string
	_ = json.Unmarshal(doc["api_key"], &key)
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		t.Fatal("rotation entropy encoding invalid")
	}
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "api_key") {
		t.Fatal("rotation response exposed key")
	}
	_ = configHTTPView(t, f)
	if w := f.request("POST", "/api/desktop/config", configControlBody("finish", map[string]any{"backup_id": tx.BackupID}), f.control, false); w.Code != 200 {
		t.Fatal("finish failed")
	}
	if w := f.request("POST", "/api/desktop/maintenance", string(payload), f.control, false); w.Code != 200 {
		t.Fatal("finished config still blocked maintenance")
	}
	end, _ := json.Marshal(map[string]any{"operation": "end", "instance_id": "config-instance", "generation": 1})
	if w := f.request("POST", "/api/desktop/maintenance", string(end), f.control, false); w.Code != 200 {
		t.Fatal("maintenance cleanup failed")
	}
}

func TestWebConfigRetainsLegacyPayload(t *testing.T) {
	f := newConfigHTTPFixture(t)
	legacy := NewServer(f.server.cfg, ops.New(f.server.cfg, f.server.svc.Store(), f.server.svc.Gateway(), upstream.New(time.Second)), http.NotFoundHandler(), "test")
	token, ok, _ := legacy.sessions.Create(legacy.cfg.UI.Username, legacy.cfg.UI.Password, legacy.cfg, "127.0.0.1")
	if !ok {
		t.Fatal("login")
	}
	f.handler, f.cookie = legacy.Handler(), token
	w := f.request("GET", "/api/config", "", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"config"`) || !strings.Contains(w.Body.String(), "CONFIG_TEST_INFERENCE_KEY") {
		t.Fatal("Web legacy config payload changed")
	}
}
