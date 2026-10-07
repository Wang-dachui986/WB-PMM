package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopControlKeyValidation(t *testing.T) {
	d := &desktopInit{Protocol: 1, InstanceID: "instance", Username: "desktop", Password: strings.Repeat("b2", 32), GatewayKey: strings.Repeat("c3", 32), GatewayURL: "http://127.0.0.1:1", AuthDir: "/tmp/synthetic-auths"}
	if err := validateDesktopInit(d); err != nil {
		t.Fatal("legacy missing key", err)
	}
	for _, key := range []string{"", "a", strings.Repeat("g", 64), d.Password, d.GatewayKey} {
		copy := *d
		copy.PanelControlKey = &key
		if err := validateDesktopInit(&copy); err == nil {
			t.Fatal("invalid control key accepted")
		}
	}
	key := strings.Repeat("a1", 32)
	d.PanelControlKey = &key
	if err := validateDesktopInit(d); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopHandlerInstallsGateBeforeHTTP(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "key"}[enabled], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "workbuddy-cmd-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			d := &desktopInit{Protocol: 1, InstanceID: "instance", Username: "desktop", Password: "synthetic-password", GatewayURL: "http://127.0.0.1:1", GatewayKey: "synthetic-gateway", AuthDir: filepath.Join(dir, "auths")}
			key := strings.Repeat("a1", 32)
			if enabled {
				d.PanelControlKey = &key
			}
			cfg, err := buildDesktopConfig(d)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := buildDesktopHandler(cfg, d)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			send := func(path, raw, control string, cookie *http.Cookie) (int, map[string]any) {
				req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(raw))
				req.Header.Set("Origin", srv.URL)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-WorkBuddy-Control-Key", control)
				req.Header.Set("X-WorkBuddy-Panel-Instance", "instance")
				req.Header.Set("X-WorkBuddy-Maintenance-Generation", "1")
				if cookie != nil {
					req.AddCookie(cookie)
				}
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var result map[string]any
				json.NewDecoder(resp.Body).Decode(&result)
				return resp.StatusCode, result
			}
			login, _ := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(`{"username":"desktop","password":"synthetic-password"}`))
			login.Header.Set("Origin", srv.URL)
			resp, err := srv.Client().Do(login)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
				t.Fatal("login")
			}
			cookie := resp.Cookies()[0]
			raw, _ := json.Marshal(map[string]any{"operation": "begin", "instance_id": "instance", "generation": 1, "action": "import", "uids": []string{"account-one"}, "expires_at": time.Now().Add(time.Minute)})
			status, _ := send("/api/desktop/maintenance", string(raw), key, nil)
			if enabled && status != 200 || !enabled && status != 401 {
				t.Fatalf("grant=%d enabled=%v", status, enabled)
			}
			status, _ = send("/api/accounts/import", `{"uid":"account-one","access_token":"synthetic-access"}`, "", cookie)
			if enabled && status != 200 || !enabled && status != 409 {
				t.Fatalf("import=%d enabled=%v", status, enabled)
			}
			files, _ := os.ReadDir(d.AuthDir)
			if enabled && len(files) != 1 || !enabled && len(files) != 0 {
				t.Fatal("credential gate bypass")
			}
			if enabled {
				status, result := send("/api/desktop/maintenance", `{"operation":"end","instance_id":"instance","generation":1}`, key, nil)
				if status != 200 || result["drained"] != true {
					t.Fatal("end")
				}
			}
		})
	}
}
