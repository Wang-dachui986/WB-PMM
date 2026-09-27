package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api-gui/internal/config"
)

func newPolicyServer(desktop bool) *Server {
	cfg := config.Default()
	cfg.UI.Username = "admin"
	cfg.UI.Password = "secret123"
	cfg.CredentialsFile = ""
	if desktop {
		return NewDesktopServer(cfg, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "test")
	}
	return NewServer(cfg, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "test")
}

func policyRequest(t *testing.T, s *Server, method, path, origin string, authenticated bool) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, nil)
	req.Host = "127.0.0.1:8787"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if authenticated {
		token, ok, _ := s.sessions.Create("admin", "secret123", s.cfg, "198.51.100.10")
		if !ok {
			t.Fatal("创建测试会话失败")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	return resp.Code, body.Code
}

func TestDesktopPolicyAuthOriginAndServerMode(t *testing.T) {
	const refresh = "/api/accounts/u1/refresh"
	const same = "http://127.0.0.1:8787"

	tests := []struct {
		name          string
		mode          bool
		origin        string
		authenticated bool
		wantStatus    int
		wantCode      string
	}{
		{name: "auth before policy", mode: true, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "desktop requires origin", mode: true, authenticated: true, wantStatus: http.StatusForbidden, wantCode: "csrf"},
		{name: "desktop rejects other origin", mode: true, origin: "https://127.0.0.1:8787", authenticated: true, wantStatus: http.StatusForbidden, wantCode: "csrf"},
		{name: "desktop rejects non exact origin", mode: true, origin: same + "/", authenticated: true, wantStatus: http.StatusForbidden, wantCode: "csrf"},
		{name: "desktop denies before handler", mode: true, origin: same, authenticated: true, wantStatus: http.StatusConflict, wantCode: "MAINTENANCE_REQUIRED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code := policyRequest(t, newPolicyServer(tt.mode), http.MethodPost, refresh, tt.origin, tt.authenticated)
			if status != tt.wantStatus || code != tt.wantCode {
				t.Fatalf("status/code = %d/%q, want %d/%q", status, code, tt.wantStatus, tt.wantCode)
			}
		})
	}

	// Server mode still lets the existing handler chain decide the result.
	s := newPolicyServer(false)
	token, ok, _ := s.sessions.Create("admin", "secret123", s.cfg, "198.51.100.10")
	if !ok {
		t.Fatal("创建服务器模式测试会话失败")
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/accounts/u1/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	called := false
	resp := httptest.NewRecorder()
	s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(resp, req)
	if !called || resp.Code != http.StatusNoContent {
		t.Fatalf("server mode handler called=%v status=%d, want called=true status=%d", called, resp.Code, http.StatusNoContent)
	}
}

func TestDesktopLoginRequiresExactOrigin(t *testing.T) {
	const path = "/api/login"
	const same = "http://127.0.0.1:8787"

	for _, tc := range []struct {
		name   string
		origin string
		status int
		code   string
	}{
		{name: "missing origin", status: http.StatusForbidden, code: "csrf"},
		{name: "wrong origin", origin: "https://127.0.0.1:8787", status: http.StatusForbidden, code: "csrf"},
		{name: "trailing slash", origin: same + "/", status: http.StatusForbidden, code: "csrf"},
		{name: "same origin reaches login", origin: same, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newPolicyServer(true)
			status, code := policyRequest(t, s, http.MethodPost, path, tc.origin, false)
			if status != tc.status || code != tc.code {
				t.Fatalf("status/code = %d/%q, want %d/%q", status, code, tc.status, tc.code)
			}
		})
	}
}

func TestDesktopLoginDoesNotExposeToken(t *testing.T) {
	s := newPolicyServer(true)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/login", strings.NewReader(`{"username":"admin","password":"secret123"}`))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("桌面登录 status=%d，want %d", resp.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析桌面登录响应失败：%v", err)
	}
	if _, ok := body["token"]; ok {
		t.Fatal("桌面登录 JSON 不应暴露 token")
	}
	if !strings.Contains(resp.Header().Get("Set-Cookie"), "wbgui_session=") {
		t.Fatal("桌面登录仍应通过 HttpOnly Cookie 建立会话")
	}
}

func TestDesktopOriginRequiresLoopbackHost(t *testing.T) {
	if !desktopOrigin("http://127.0.0.1:8787", "127.0.0.1:8787") {
		t.Fatal("合法回环 Host 应通过桌面 Origin 校验")
	}
	for _, host := range []string{"example.com:8787", "192.168.1.20:8787", "127.0.0.1"} {
		if desktopOrigin("http://"+host, host) {
			t.Fatalf("非合法回环 Host %q 不应通过桌面 Origin 校验", host)
		}
	}
}

func TestServerLoginRetainsToken(t *testing.T) {
	s := newPolicyServer(false)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/login", strings.NewReader(`{"username":"admin","password":"secret123"}`))
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("服务器登录 status=%d，want %d", resp.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析服务器登录响应失败：%v", err)
	}
	if token, ok := body["token"].(string); !ok || token == "" {
		t.Fatal("服务器模式登录应继续返回 token")
	}
}

func TestDesktopPolicyRouteClasses(t *testing.T) {
	cases := []struct {
		method string
		path   string
		status int
		code   string
	}{
		{http.MethodPost, "/api/accounts/u1/checkin", http.StatusForbidden, "DESKTOP_OPERATION_DISABLED"},
		{http.MethodPost, "/api/accounts/import", http.StatusConflict, "MAINTENANCE_REQUIRED"},
		{http.MethodDelete, "/api/accounts/u1", http.StatusConflict, "MAINTENANCE_REQUIRED"},
		{http.MethodGet, "/api/accounts/u1", http.StatusConflict, "MAINTENANCE_REQUIRED"},
		{http.MethodGet, "/api/accounts/u1?silent=true", 0, ""},
		{http.MethodPut, "/api/config", http.StatusForbidden, "DESKTOP_CONFIG_DISABLED"},
		{http.MethodGet, "/api/system", http.StatusForbidden, "DESKTOP_NATIVE_ONLY"},
		{http.MethodPost, "/api/stats/reset", http.StatusForbidden, "DESKTOP_NATIVE_ONLY"},
		{http.MethodPost, "/api/login/session-1/poll", http.StatusConflict, "MAINTENANCE_REQUIRED"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			status, code, _, _ := desktopPolicy(httptest.NewRequest(tc.method, "http://127.0.0.1:8787"+tc.path, nil))
			if status != tc.status || code != tc.code {
				t.Fatalf("status/code = %d/%q, want %d/%q", status, code, tc.status, tc.code)
			}
		})
	}
}
