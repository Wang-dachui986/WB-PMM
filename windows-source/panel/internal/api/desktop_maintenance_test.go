package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/config"
	"workbuddy2api-gui/internal/ops"
	"workbuddy2api-gui/internal/upstream"
)

type maintenanceTransport func(*http.Request) (*http.Response, error)

func (f maintenanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func maintenanceJSON(data string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":` + data + `}`))}, nil
}

type maintenanceHTTPFixture struct {
	t          *testing.T
	server     *Server
	http       *httptest.Server
	svc        *ops.Service
	cookie     string
	key        string
	generation uint64
	account    *authstore.Account
}

func newMaintenanceHTTPFixture(t *testing.T, key bool, transport maintenanceTransport) *maintenanceHTTPFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "workbuddy-http-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	store, err := authstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &authstore.Account{UID: "account-one", AccessToken: "SYNTHETIC_OLD_ACCESS", RefreshToken: "SYNTHETIC_REFRESH", ExpiresAt: 1}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.UI.Username, cfg.UI.Password, cfg.GatewayAPIKey = "desktop", "SYNTHETIC_PANEL_PASSWORD", "SYNTHETIC_INFERENCE_KEY"
	cfg.PricingFile = ""
	up := upstream.New(time.Second)
	if transport == nil {
		transport = func(*http.Request) (*http.Response, error) {
			t.Error("unexpected upstream")
			return nil, errors.New("no upstream")
		}
	}
	up.HTTP.Transport = transport
	svc := ops.New(cfg, store, nil, up)
	s := NewDesktopServer(cfg, svc, http.NotFoundHandler(), "test")
	control := ""
	if key {
		control = strings.Repeat("a1", 32)
	}
	if err := s.ConfigureDesktopMaintenance("panel-test", control); err != nil {
		t.Fatal(err)
	}
	f := &maintenanceHTTPFixture{t: t, server: s, svc: svc, key: control, account: a}
	f.http = httptest.NewServer(s.Handler())
	f.http.Client().Timeout = 3 * time.Second
	token, ok, _ := s.sessions.Create(cfg.UI.Username, cfg.UI.Password, cfg, "127.0.0.1")
	if !ok {
		t.Fatal("session")
	}
	f.cookie = token
	t.Cleanup(func() {
		if f.generation > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = svc.RevokeMaintenance(ctx, "panel-test", f.generation)
		}
		f.http.Close()
	})
	return f
}

func (f *maintenanceHTTPFixture) request(method, path, body string, control, cookie bool, gen string, mutate func(*http.Request)) (int, map[string]any, string) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Origin", f.http.URL)
	req.Header.Set("Content-Type", "application/json")
	if control {
		req.Header.Set("X-WorkBuddy-Control-Key", f.key)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.cookie})
	}
	if gen != "" {
		req.Header.Set("X-WorkBuddy-Panel-Instance", "panel-test")
		req.Header.Set("X-WorkBuddy-Maintenance-Generation", gen)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return resp.StatusCode, value, string(raw)
}
func (f *maintenanceHTTPFixture) begin(action string, uids []string, gen uint64, expiry time.Time) {
	f.t.Helper()
	raw, _ := json.Marshal(map[string]any{"operation": "begin", "instance_id": "panel-test", "generation": gen, "action": action, "uids": uids, "expires_at": expiry})
	status, body, _ := f.request("POST", "/api/desktop/maintenance", string(raw), true, false, "", nil)
	if status != 200 || body["ok"] != true || body["instance_id"] != "panel-test" {
		f.t.Fatalf("grant: %d %#v", status, body)
	}
	f.generation = gen
}
func requireHTTP(t *testing.T, got int, body map[string]any, status int, code string) {
	t.Helper()
	if got != status || (code != "" && body["code"] != code) {
		t.Fatalf("response=%d %#v; want=%d %s", got, body, status, code)
	}
}

func TestMaintenanceHTTPIndependentControlAndStrictJSON(t *testing.T) {
	f := newMaintenanceHTTPFixture(t, true, nil)
	body := `{"operation":"end","instance_id":"panel-test","generation":1}`
	cases := []struct {
		name, key string
		cookie    bool
	}{
		{"missing", "", false}, {"cookie only", "", true}, {"password", f.server.cfg.UI.Password, true},
		{"inference", f.server.cfg.GatewayAPIKey, true}, {"session", f.cookie, true}, {"wrong", strings.Repeat("b2", 32), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, v, _ := f.request("POST", "/api/desktop/maintenance", body, false, tc.cookie, "", func(r *http.Request) { r.Header.Set("X-WorkBuddy-Control-Key", tc.key) })
			requireHTTP(t, status, v, 401, "CONTROL_UNAUTHORIZED")
		})
	}
	for _, origin := range []string{"", "https://127.0.0.1:1", f.http.URL + "/"} {
		status, v, _ := f.request("POST", "/api/desktop/maintenance", body, true, false, "", func(r *http.Request) { r.Header.Set("Origin", origin) })
		requireHTTP(t, status, v, 403, "csrf")
	}
	for _, raw := range []string{"null", "[]", body + ` {}`, strings.Replace(body, `"generation":1`, `"generation":1,"generation":2`, 1), strings.Replace(body, `"generation":1`, `"generation":1,"action":null`, 1), strings.Replace(body, `"generation":1`, `"generation":"1"`, 1), strings.Replace(body, `"generation":1`, `"generation":1,"unknown":"SYNTHETIC_SECRET"`, 1), strings.Repeat(" ", 1<<16) + body} {
		status, v, rawReply := f.request("POST", "/api/desktop/maintenance", raw, true, false, "", nil)
		requireHTTP(t, status, v, 400, "INVALID_REQUEST")
		if strings.Contains(rawReply, "SYNTHETIC_SECRET") {
			t.Fatal("decoder echoed secret")
		}
	}
	status, value, _ := f.request("POST", "/api/desktop/maintenance", body, true, false, "", func(r *http.Request) {
		r.Trailer = http.Header{"X-Extra": []string{"SYNTHETIC_SECRET"}}
		r.ContentLength = -1
	})
	requireHTTP(t, status, value, 400, "INVALID_REQUEST")

	f.begin("delete", []string{"account-one"}, 9007199254740993, time.Now().Add(time.Minute))
	raw := `{"operation":"end","instance_id":"panel-test","generation":9007199254740993}`
	status, v, reply := f.request("POST", "/api/desktop/maintenance", raw, true, false, "", nil)
	requireHTTP(t, status, v, 200, "")
	if v["drained"] != true || !strings.Contains(reply, `"generation":9007199254740993`) {
		t.Fatal("lost uint64 or missing drain")
	}
}

func TestMaintenanceHTTPBusinessBindingAndLegacyClosed(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "control"}[enabled], func(t *testing.T) {
			f := newMaintenanceHTTPFixture(t, enabled, nil)
			if enabled {
				f.begin("delete", []string{"account-one"}, 1, time.Now().Add(time.Minute))
			}
			status, v, _ := f.request("DELETE", "/api/accounts/account-one?confirm=account-one", "", false, true, "", nil)
			requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
			for _, gen := range []string{"0", "01", "-1", "2", "18446744073709551616"} {
				status, v, _ = f.request("DELETE", "/api/accounts/account-one?confirm=account-one", "", false, true, gen, nil)
				requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
			}
			status, v, _ = f.request("DELETE", "/api/accounts/account-one?confirm=account-one", "", false, true, "1", func(r *http.Request) { r.Header.Set("X-WorkBuddy-Panel-Instance", "old-panel") })
			requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
			if enabled {
				status, v, _ = f.request("DELETE", "/api/accounts/account-one?confirm=account-one", "", false, false, "1", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.cookie) })
				requireHTTP(t, status, v, 401, "unauthorized")
				status, v, _ = f.request("POST", "/api/accounts/account-one/refresh", "{}", false, true, "1", nil)
				requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
				status, v, _ = f.request("DELETE", "/api/accounts/account-one?confirm=account-one", "", false, true, "1", nil)
				requireHTTP(t, status, v, 200, "")
			}
			for _, path := range []string{"/api/accounts/account-one/checkin", "/api/accounts/account-one/travel", "/api/tasks/checkin", "/api/config/reset", "/api/stats/reset"} {
				status, _, _ := f.request("POST", path, "{}", false, true, "1", nil)
				if status != 403 {
					t.Fatalf("disabled %s: %d", path, status)
				}
			}
		})
	}
}

func TestMaintenanceHTTPDrainWaitsForLateWrite(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
		entered <- r.Context()
		<-release
		return maintenanceJSON(`{"accessToken":"SYNTHETIC_LATE_ACCESS","expiresIn":3600}`)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	original, _ := os.ReadFile(f.account.FilePath)
	f.begin("refresh", []string{"account-one"}, 1, time.Now().Add(time.Minute))
	status, v, _ := f.request("POST", "/api/tasks/refresh", `{"uids":["account-one"]}`, false, true, "1", nil)
	requireHTTP(t, status, v, 202, "")
	taskCtx := <-entered
	ended := make(chan int, 1)
	go func() {
		status, _, _ := f.request("POST", "/api/desktop/maintenance", `{"operation":"end","instance_id":"panel-test","generation":1}`, true, false, "", nil)
		ended <- status
	}()
	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("end did not cancel")
	}
	select {
	case <-ended:
		t.Fatal("end returned while task could still write")
	default:
	}
	status, v, _ = f.request("POST", "/api/accounts/account-one/refresh", "{}", false, true, "1", nil)
	requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
	close(release)
	select {
	case status := <-ended:
		if status != 200 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("did not drain")
	}
	after, _ := os.ReadFile(f.account.FilePath)
	if !bytes.Equal(after, original) {
		t.Fatal("late response changed credentials")
	}
	f.begin("delete", []string{"account-one"}, 2, time.Now().Add(time.Minute))
	status, v, _ = f.request("POST", "/api/desktop/maintenance", `{"operation":"end","instance_id":"panel-test","generation":1}`, true, false, "", nil)
	requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
}

func TestMaintenanceHTTPPublicLoginPrivateLookupAndExpiry(t *testing.T) {
	var calls atomic.Int32
	f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return maintenanceJSON(`{"state":"SYNTHETIC_OAUTH_STATE","authUrl":"https://copilot.tencent.com/login?state=SYNTHETIC_OAUTH_STATE"}`)
	})
	expiry := time.Now().Add(250 * time.Millisecond)
	f.begin("login", []string{}, 1, expiry)
	status, v, raw := f.request("POST", "/api/login/start", `{"region":"cn"}`, false, true, "1", nil)
	requireHTTP(t, status, v, 200, "")
	id := v["id"].(string)
	if strings.Contains(raw, "auth_url") || strings.Contains(raw, "SYNTHETIC_OAUTH_STATE") {
		t.Fatal("public auth URL escaped")
	}
	status, v, _ = f.request("GET", "/api/desktop/login/"+id, "", false, true, "1", nil)
	requireHTTP(t, status, v, 401, "CONTROL_UNAUTHORIZED")
	status, v, _ = f.request("GET", "/api/desktop/login/"+id, "", true, false, "", nil)
	requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
	status, v, _ = f.request("GET", "/api/desktop/login/"+id, "", true, false, "1", nil)
	requireHTTP(t, status, v, 200, "")
	if len(v) != 3 || v["session_id"] != id || !strings.Contains(v["auth_url"].(string), "SYNTHETIC_OAUTH_STATE") {
		t.Fatalf("private schema %#v", v)
	}
	expires, _ := time.Parse(time.RFC3339Nano, v["expires_at"].(string))
	if !expires.Equal(expiry) {
		t.Fatal("expiry not lease bounded")
	}
	status, v, _ = f.request("GET", "/api/login/not-found", "", false, true, "1", nil)
	requireHTTP(t, status, v, 404, "LOGIN_NOT_FOUND")
	time.Sleep(time.Until(expiry) + 5*time.Millisecond)
	for _, path := range []string{"/api/desktop/login/" + id, "/api/login/" + id} {
		status, v, _ = f.request("GET", path, "", strings.Contains(path, "desktop"), true, "1", nil)
		requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected upstream")
	}
}

func TestMaintenanceHTTPDetailIsLocalAndErrorSecretsAreHidden(t *testing.T) {
	var calls atomic.Int32
	f := newMaintenanceHTTPFixture(t, true, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("SYNTHETIC_TRANSPORT_SECRET")
	})
	status, v, raw := f.request("GET", "/api/accounts/account-one", "", false, true, "", nil)
	requireHTTP(t, status, v, 200, "")
	if calls.Load() != 0 || strings.Contains(raw, "SYNTHETIC_") {
		t.Fatal("detail refreshed or leaked credentials")
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	f.begin("login", []string{}, 1, time.Now().Add(time.Minute))
	status, v, raw = f.request("POST", "/api/login/start", `{"region":"cn"}`, false, true, "1", nil)
	requireHTTP(t, status, v, 500, "LOGIN_INTERNAL")
	for _, text := range []string{raw, logs.String()} {
		for _, secret := range []string{"SYNTHETIC_TRANSPORT_SECRET", f.key, f.cookie, f.server.cfg.UI.Password, f.server.cfg.GatewayAPIKey} {
			if strings.Contains(text, secret) {
				t.Fatal("secret in public response/log")
			}
		}
	}
}

func TestMaintenanceHTTPStableErrors(t *testing.T) {
	s := &Server{desktop: true}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ops.ErrLoginBusy, 409, "LOGIN_BUSY"}, {ops.ErrLoginOverwriteNotPending, 409, "LOGIN_OVERWRITE_NOT_PENDING"},
		{ops.ErrCredentialRegionConflict, 409, "CREDENTIAL_REGION_CONFLICT"}, {ops.ErrLoginInternal, 500, "LOGIN_INTERNAL"},
		{ops.ErrLoginNotFound, 404, "LOGIN_NOT_FOUND"},
	} {
		w := httptest.NewRecorder()
		s.writeLoginError(w, tc.err)
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		requireHTTP(t, w.Code, v, tc.status, tc.code)
	}
}

func TestMaintenanceHTTPLoginOverwriteConfirmation(t *testing.T) {
	for _, region := range []string{"cn", "global"} {
		t.Run(region, func(t *testing.T) {
			var polls atomic.Int32
			f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v2/plugin/auth/state":
					return maintenanceJSON(`{"state":"SYNTHETIC_STATE"}`)
				case "/v2/plugin/auth/token":
					polls.Add(1)
					return maintenanceJSON(`{"accessToken":"SYNTHETIC_NEW_ACCESS","refreshToken":"SYNTHETIC_NEW_REFRESH"}`)
				default:
					return maintenanceJSON(`{"uid":"account-one","nickname":"new nickname"}`)
				}
			})
			old, _ := os.ReadFile(f.account.FilePath)
			f.begin("login", []string{"account-one"}, 1, time.Now().Add(time.Minute))
			status, v, _ := f.request("POST", "/api/login/start", `{"region":"`+region+`"}`, false, true, "1", nil)
			requireHTTP(t, status, v, 200, "")
			id := v["id"].(string)
			status, v, _ = f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
			if region == "global" {
				requireHTTP(t, status, v, 409, "CREDENTIAL_REGION_CONFLICT")
				after, _ := os.ReadFile(f.account.FilePath)
				if !bytes.Equal(old, after) {
					t.Fatal("region conflict overwrote")
				}
				return
			}
			requireHTTP(t, status, v, 200, "")
			if v["requires_overwrite_confirmation"] != true || v["saved"] != false {
				t.Fatalf("no explicit confirmation %#v", v)
			}
			after, _ := os.ReadFile(f.account.FilePath)
			if !bytes.Equal(old, after) {
				t.Fatal("unconfirmed overwrite")
			}
			for _, body := range []string{`{"uid":"account-other"}`, `{"uid":"account-one","extra":true}`} {
				status, v, _ = f.request("POST", "/api/login/"+id+"/confirm-overwrite", body, false, true, "1", nil)
				if status != 400 && status != 409 {
					t.Fatal("invalid confirmation", status, v)
				}
			}
			status, v, _ = f.request("POST", "/api/login/"+id+"/confirm-overwrite", `{"uid":"account-one"}`, false, true, "1", nil)
			requireHTTP(t, status, v, 200, "")
			status, v, raw := f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
			requireHTTP(t, status, v, 200, "")
			if v["saved"] != true || strings.Contains(raw, "SYNTHETIC_") || polls.Load() != 1 {
				t.Fatalf("save/retry bad %#v", v)
			}
			status, v, _ = f.request("GET", "/api/desktop/login/"+id, "", true, false, "1", nil)
			requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
			_, _, _ = f.request("POST", "/api/desktop/maintenance", `{"operation":"end","instance_id":"panel-test","generation":1}`, true, false, "", nil)
			f.begin("login", []string{}, 2, time.Now().Add(time.Minute))
			status, v, _ = f.request("POST", "/api/login/"+id+"/cancel", "{}", false, true, "2", nil)
			requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
		})
	}
}

func TestMaintenanceHTTPCNLoginDomainSave(t *testing.T) {
	for _, tc := range []struct {
		domain, uid, saveCode string
	}{
		{"codebuddy.cn", "account-new", ""},
		{"www.codebuddy.cn", "account-one", ""},
		{"unknown.example", "account-new", "CREDENTIAL_DOMAIN_UNSUPPORTED"},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			var polls atomic.Int32
			f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v2/plugin/auth/state":
					return maintenanceJSON(`{"state":"SYNTHETIC_STATE"}`)
				case "/v2/plugin/auth/token":
					polls.Add(1)
					return maintenanceJSON(`{"accessToken":"SYNTHETIC_ACCESS","domain":"` + tc.domain + `"}`)
				default:
					return maintenanceJSON(`{"uid":"` + tc.uid + `","nickname":"synthetic"}`)
				}
			})
			f.begin("login", []string{}, 1, time.Now().Add(time.Minute))
			status, v, _ := f.request("POST", "/api/login/start", `{"region":"cn"}`, false, true, "1", nil)
			requireHTTP(t, status, v, 200, "")
			id := v["id"].(string)
			status, v, raw := f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
			requireHTTP(t, status, v, 200, "")
			if tc.uid == "account-one" {
				if v["saved"] != false || v["save_error_code"] != "OVERWRITE_CONFIRMATION_REQUIRED" {
					t.Fatalf("overwrite confirmation missing: %#v", v)
				}
				status, v, _ = f.request("POST", "/api/login/"+id+"/confirm-overwrite", `{"uid":"account-one"}`, false, true, "1", nil)
				requireHTTP(t, status, v, 200, "")
				status, v, raw = f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
				requireHTTP(t, status, v, 200, "")
			}
			if v["saved"] != (tc.saveCode == "") || v["save_error_code"] != tc.saveCode || strings.Contains(raw, "SYNTHETIC_ACCESS") || strings.Contains(raw, tc.domain) {
				t.Fatalf("unsafe or incorrect save result: %#v", v)
			}
			if polls.Load() != 1 {
				t.Fatal("save retry fetched credentials again")
			}
		})
	}
}

func TestMaintenanceHTTPRevokeTimeoutStaysClosed(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
		entered <- r.Context()
		<-release
		return maintenanceJSON(`{"accessToken":"late","expiresIn":3600}`)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.begin("refresh", []string{"account-one"}, 1, time.Now().Add(time.Minute))
	status, v, _ := f.request("POST", "/api/tasks/refresh", `{"uids":["account-one"]}`, false, true, "1", nil)
	requireHTTP(t, status, v, 202, "")
	taskCtx := <-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", f.http.URL+"/api/desktop/maintenance", strings.NewReader(`{"operation":"end","instance_id":"panel-test","generation":1}`))
	req.Header.Set("Origin", f.http.URL)
	req.Header.Set("X-WorkBuddy-Control-Key", f.key)
	resp, err := f.http.Client().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("end returned before worker")
	}
	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("not cancelled")
	}
	raw, _ := json.Marshal(map[string]any{"operation": "begin", "instance_id": "panel-test", "generation": 2, "action": "delete", "uids": []string{"account-one"}, "expires_at": time.Now().Add(time.Minute)})
	status, v, _ = f.request("POST", "/api/desktop/maintenance", string(raw), true, false, "", nil)
	requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
	close(release)
	status, v, _ = f.request("POST", "/api/desktop/maintenance", `{"operation":"end","instance_id":"panel-test","generation":1}`, true, false, "", nil)
	requireHTTP(t, status, v, 200, "")
	if v["drained"] != true {
		t.Fatal("missing drain ack")
	}
}

func TestMaintenanceHTTPWebLoginKeepsAuthURL(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "workbuddy-web-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := authstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.UI.Username = "web"
	cfg.UI.Password = "synthetic-password"
	cfg.PricingFile = ""
	up := upstream.New(time.Second)
	up.HTTP.Transport = maintenanceTransport(func(*http.Request) (*http.Response, error) {
		return maintenanceJSON(`{"state":"synthetic-state","authUrl":"https://example.invalid/legacy"}`)
	})
	svc := ops.New(cfg, store, nil, up)
	s := NewServer(cfg, svc, http.NotFoundHandler(), "test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	token, _, _ := s.sessions.Create(cfg.UI.Username, cfg.UI.Password, cfg, "127.0.0.1")
	req, _ := http.NewRequest("POST", srv.URL+"/api/login/start", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 || result["auth_url"] != "https://example.invalid/legacy" {
		t.Fatalf("web changed %#v", result)
	}
	if _, err := svc.CancelLogin(result["id"].(string)); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceHTTPCancelBusyPollAndLeaseBinding(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	f := newMaintenanceHTTPFixture(t, true, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v2/plugin/auth/state" {
			return maintenanceJSON(`{"state":"SYNTHETIC_STATE"}`)
		}
		entered <- r.Context()
		<-release
		return maintenanceJSON(`{}`)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.begin("login", []string{}, 1, time.Now().Add(time.Minute))
	status, v, _ := f.request("POST", "/api/login/start", `{}`, false, true, "1", nil)
	requireHTTP(t, status, v, 200, "")
	id := v["id"].(string)
	returned := make(chan map[string]any, 1)
	go func() {
		_, v, _ := f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
		returned <- v
	}()
	opCtx := <-entered
	status, v, _ = f.request("POST", "/api/login/"+id+"/poll", "{}", false, true, "1", nil)
	requireHTTP(t, status, v, 409, "LOGIN_BUSY")
	status, v, _ = f.request("POST", "/api/login/"+id+"/cancel", "{}", false, true, "1", nil)
	requireHTTP(t, status, v, 200, "")
	if v["status"] != "cancelled" {
		t.Fatal("not cancelled")
	}
	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("transport not cancelled")
	}
	status, v, _ = f.request("GET", "/api/desktop/login/"+id, "", true, false, "1", nil)
	requireHTTP(t, status, v, 409, "MAINTENANCE_REQUIRED")
	close(release)
	select {
	case v := <-returned:
		if v["saved"] != false || v["status"] != "cancelled" {
			t.Fatal("late poll revived")
		}
	case <-time.After(time.Second):
		t.Fatal("poll not returned")
	}
}

func TestMaintenanceHTTPReadSnapshotBrowserOriginSemantics(t *testing.T) {
	var calls atomic.Int32
	f := newMaintenanceHTTPFixture(t, true, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return maintenanceJSON(`{"state":"SYNTHETIC_STATE"}`)
	})
	f.begin("login", []string{}, 1, time.Now().Add(time.Minute))
	status, v, _ := f.request("POST", "/api/login/start", `{}`, false, true, "1", nil)
	requireHTTP(t, status, v, 200, "")
	id := v["id"].(string)
	for _, tc := range []struct {
		name, origin, ref string
		originPresent     bool
		status            int
	}{
		{name: "browser GET", ref: f.http.URL + "/accounts?tab=login", status: 200},
		{name: "no evidence", status: 403},
		{name: "foreign", ref: "http://example.invalid/", status: 403},
		{name: "userinfo", ref: strings.Replace(f.http.URL, "http://", "http://user@", 1) + "/", status: 403},
		{name: "prefix", ref: f.http.URL + ".attacker.invalid/", status: 403},
		{name: "other port", ref: f.http.URL + "0/", status: 403},
		{name: "wrong scheme", ref: strings.Replace(f.http.URL, "http://", "https://", 1) + "/", status: 403},
		{name: "bad origin good referer", origin: "http://example.invalid", originPresent: true, ref: f.http.URL + "/", status: 403},
		{name: "empty origin good referer", originPresent: true, ref: f.http.URL + "/", status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, v, _ := f.request("GET", "/api/login/"+id, "", false, true, "1", func(r *http.Request) {
				r.Header.Del("Origin")
				if tc.originPresent {
					r.Header.Set("Origin", tc.origin)
				}
				if tc.ref != "" {
					r.Header.Set("Referer", tc.ref)
				}
			})
			code := ""
			if tc.status == 403 {
				code = "csrf"
			}
			requireHTTP(t, status, v, tc.status, code)
		})
	}
	for _, path := range []string{"/api/desktop/login/" + id, "/api/login/" + id + "/poll"} {
		method := "GET"
		if strings.HasSuffix(path, "poll") {
			method = "POST"
		}
		status, v, _ := f.request(method, path, "{}", true, true, "1", func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Referer", f.http.URL+"/") })
		requireHTTP(t, status, v, 403, "csrf")
	}
	if calls.Load() != 1 {
		t.Fatal("GET snapshot performed upstream work")
	}
}
