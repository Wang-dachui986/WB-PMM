package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
)

// modelIDsFromRecorder 解析 /v1/models 响应的 id 集合。
func modelIDsFromRecorder(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /v1/models: %v body=%s", err, rec.Body.String())
	}
	ids := map[string]bool{}
	for _, m := range body.Data {
		ids[m.ID] = true
	}
	return ids
}

// TestModelsRefreshBypassesCNCache 上游新上架模型：普通请求命中 1h 缓存看不到，
// ?refresh=1 跳过缓存立即可见。
func TestModelsRefreshBypassesCNCache(t *testing.T) {
	var launched atomic.Bool
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if launched.Load() {
			return 200, `{"code":0,"data":{"models":[
				{"id":"old-model","maxInputTokens":65536,"maxOutputTokens":8192},
				{"id":"new-model","maxInputTokens":65536,"maxOutputTokens":8192}
			],"agents":[{"name":"cli","models":["old-model","new-model"]}]}}`, false
		}
		return 200, `{"code":0,"data":{"models":[
			{"id":"old-model","maxInputTokens":65536,"maxOutputTokens":8192}
		],"agents":[{"name":"cli","models":["old-model"]}]}}`, false
	})
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: false})

	get := func(target string) map[string]bool {
		rec := httptest.NewRecorder()
		h.models(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, rec.Code)
		}
		return modelIDsFromRecorder(t, rec)
	}

	if ids := get("/v1/models"); !ids["cn:old-model"] || ids["cn:new-model"] {
		t.Fatalf("initial list=%v want only cn:old-model", ids)
	}
	launched.Store(true)
	if ids := get("/v1/models"); ids["cn:new-model"] {
		t.Fatalf("cached list should not see new-model yet: %v", ids)
	}
	if ids := get("/v1/models?refresh=1"); !ids["cn:new-model"] {
		t.Fatalf("refresh=1 list=%v want cn:new-model", ids)
	}
	// 刷新结果写回缓存：后续普通请求直接可见。
	if ids := get("/v1/models"); !ids["cn:new-model"] {
		t.Fatalf("after refresh cached list=%v want cn:new-model", ids)
	}
}

// TestModelsRefreshBypassesNegativeCache 上次拉取失败进入 5min 负缓存时，
// ?refresh=1 也立即重拉。
func TestModelsRefreshBypassesNegativeCache(t *testing.T) {
	var healthy atomic.Bool
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if !healthy.Load() {
			return 500, `{"code":1}`, false
		}
		return 200, `{"code":0,"data":{"models":[
			{"id":"ok-model","maxInputTokens":65536,"maxOutputTokens":8192}
		],"agents":[{"name":"cli","models":["ok-model"]}]}}`, false
	})
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: false})

	if got := h.modelList(); len(got) != 0 {
		t.Fatalf("failing upstream list=%v want empty", got)
	}
	healthy.Store(true)
	if got := h.modelList(); len(got) != 0 {
		t.Fatalf("negative cache should hold: %v", got)
	}
	rec := httptest.NewRecorder()
	h.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models?refresh=true", nil))
	if ids := modelIDsFromRecorder(t, rec); !ids["cn:ok-model"] {
		t.Fatalf("refresh=true list=%v want cn:ok-model", ids)
	}
}

// TestModelsRefreshReprobesGlobal ?refresh=1 让 global 探测缓存失效并重新探测；
// 普通请求在 TTL 内不重复探测。
func TestModelsRefreshReprobesGlobal(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	cf := newGlobalModelsHandlerFake(t, 200, `{"code":0,"data":["gpt-5.4"]}`)
	p := testPoolWith(&auth.Auth{UID: "g1", AccessToken: "at_gl", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: cf.up, GlobalEnabled: true})

	h.modelList()
	first, _, _, _ := cf.snapshot()
	if first == 0 {
		t.Fatal("first listing should probe global")
	}
	h.modelList()
	if again, _, _, _ := cf.snapshot(); again != first {
		t.Fatalf("cached listing re-probed: before=%d after=%d", first, again)
	}
	rec := httptest.NewRecorder()
	h.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models?refresh=1", nil))
	if after, _, _, _ := cf.snapshot(); after <= first {
		t.Fatalf("refresh=1 should re-probe global: before=%d after=%d", first, after)
	}
	if ids := modelIDsFromRecorder(t, rec); !ids["global:gpt-5.4"] {
		t.Fatalf("refresh list=%v want global:gpt-5.4", ids)
	}
}

// TestForceRefreshParam 只认 refresh=1/true（大小写不敏感），其余值不触发。
func TestForceRefreshParam(t *testing.T) {
	cases := map[string]bool{
		"/v1/models":              false,
		"/v1/models?refresh=1":    true,
		"/v1/models?refresh=TRUE": true,
		"/v1/models?refresh=0":    false,
		"/v1/models?refresh=":     false,
		"/v1/models?refresh=yes":  false,
	}
	for target, want := range cases {
		if got := forceRefresh(httptest.NewRequest(http.MethodGet, target, nil)); got != want {
			t.Errorf("forceRefresh(%s)=%v want %v", target, got, want)
		}
	}
}
