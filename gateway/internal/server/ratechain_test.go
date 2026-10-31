package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// withRateEventFile 把 429 事件落盘指向临时目录，返回事件文件路径。
func withRateEventFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	StartRateEventLog(filepath.Join(dir, "state.json"))
	t.Cleanup(func() { StartRateEventLog("") })
	return filepath.Join(dir, "ratelimit-events.jsonl")
}

// readRateEvents 读取事件文件的全部行。
func readRateEvents(t *testing.T, path string) []rateEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var out []rateEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev rateEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// resetBody 构造「将在 now+d 重置」的上游 429 文案（code 可选 6004 模型级）。
func resetBody(code int, d time.Duration) string {
	ts := time.Now().Add(d).In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	return `{"code":` + itoa(code) + `,"msg":"使用量已超出频率限制，将在 ` + ts + ` UTC+8 重置","requestId":"req-1"}`
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func threeAccountPool() []*auth.Auth {
	return []*auth.Auth{
		{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
		{UID: "u3", AccessToken: "at3", ExpiresAt: 9999999999},
	}
}

// TestRateChainGateThresholdAndModelIsolation 单号反复不触发；第二个不同号触发；
// 激活只作用于该模型，其他模型不受影响。
func TestRateChainGateThresholdAndModelIsolation(t *testing.T) {
	var g rateChainGate
	for n := 0; n < 5; n++ {
		if g.noteLongRate("kimi", "u1") {
			t.Fatalf("single account must never trigger, iter=%d", n)
		}
	}
	if !g.noteLongRate("kimi", "u2") {
		t.Fatal("second distinct account must activate")
	}
	if !g.active("kimi") || g.active("deepseek") {
		t.Fatalf("activation must be per model: kimi=%t deepseek=%t", g.active("kimi"), g.active("deepseek"))
	}
	if g.noteLongRate("deepseek", "u3") {
		t.Fatal("other model's first hit must not be chained")
	}
}

// TestRateChainGateWindowExpiry 激活窗口到期自然解除，旧账清空。
func TestRateChainGateWindowExpiry(t *testing.T) {
	prev := rateChainWindow
	rateChainWindow = 100 * time.Millisecond
	t.Cleanup(func() { rateChainWindow = prev })
	var g rateChainGate
	g.noteLongRate("kimi", "u1")
	g.noteLongRate("kimi", "u2")
	if !g.active("kimi") {
		t.Fatal("must activate")
	}
	time.Sleep(150 * time.Millisecond)
	if g.active("kimi") {
		t.Fatal("must expire after window")
	}
	if g.noteLongRate("kimi", "u3") {
		t.Fatal("fresh detection after expiry needs two distinct accounts again")
	}
}

// TestChatRateChainStopsRotation 3 个号全回「2 小时后重置」的 429：第二个号触发
// 连锁保护即停止换号（上游调用 2 次，u3 不被拖下水），回客户端 429 并透传原文；
// 两次 429 原文都落盘。
func TestChatRateChainStopsRotation(t *testing.T) {
	path := withRateEventFile(t)
	body := resetBody(11140, 2*time.Hour)
	var calls atomic.Int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls.Add(1)
		return 429, body, false
	})
	p := testPoolWith(threeAccountPool()...)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3.1-pro","messages":[]}`)))

	if n := calls.Load(); n != 2 {
		t.Fatalf("upstream calls=%d want 2 (chain guard stops at threshold)", n)
	}
	if rec.Code != 429 {
		t.Fatalf("code=%d want 429 body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "req-1") {
		t.Errorf("upstream 429 body must passthrough: %s", rec.Body)
	}
	if st, _ := p.Status("u3"); st.Cooling {
		t.Errorf("u3 must not be cooled (never called): %+v", st)
	}
	evs := readRateEvents(t, path)
	if len(evs) != 2 {
		t.Fatalf("events=%d want 2: %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Model != "kimi-k3.1-pro" || ev.Status != 429 || !ev.LongReset || ev.ModelLevel || ev.ResetAt == "" || !strings.Contains(ev.Body, "将在") {
			t.Errorf("event fields wrong: %+v", ev)
		}
	}
	if evs[0].ChainStop || !evs[1].ChainStop {
		t.Errorf("only the threshold-crossing event stops the chain: %+v", evs)
	}
}

// TestChatShortReset429StillRotates 短重置（1 分钟）429 是普通频控：不触发连锁
// 保护，照常换满 MaxRotate 个号；原文仍落盘。
func TestChatShortReset429StillRotates(t *testing.T) {
	path := withRateEventFile(t)
	body := resetBody(11140, time.Minute)
	var calls atomic.Int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls.Add(1)
		return 429, body, false
	})
	h := NewHandler(Config{Pool: testPoolWith(threeAccountPool()...), Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3.1-pro","messages":[]}`)))
	if n := calls.Load(); n != 3 {
		t.Fatalf("upstream calls=%d want 3 (short reset keeps rotating)", n)
	}
	evs := readRateEvents(t, path)
	if len(evs) != 3 {
		t.Fatalf("events=%d want 3", len(evs))
	}
	for _, ev := range evs {
		if ev.LongReset || ev.ChainStop {
			t.Errorf("short reset must not count as chain: %+v", ev)
		}
	}
}

// TestChatModelLevel429LoggedAsModelLevel 6004 模型级限流：事件 model_level=true，
// 便于判断限流是否只针对该模型。
func TestChatModelLevel429LoggedAsModelLevel(t *testing.T) {
	path := withRateEventFile(t)
	body := resetBody(6004, 2*time.Hour)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at1" {
			return 429, body, false
		}
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	// 多发几次，确保至少一次落在 u1 上。
	for i := 0; i < 6; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3.1-pro","messages":[]}`)))
		if rec.Code != 200 {
			t.Fatalf("single-account 6004 must rotate to healthy account: code=%d", rec.Code)
		}
		if _, err := os.Stat(path); err == nil {
			break
		}
	}
	evs := readRateEvents(t, path)
	if len(evs) != 1 || !evs[0].ModelLevel || evs[0].ChainStop {
		t.Fatalf("want one model-level non-chain event, got %+v", evs)
	}
}

// TestRateEventLogRotates 超过上限时把旧文件轮转为 .1。
func TestRateEventLogRotates(t *testing.T) {
	path := withRateEventFile(t)
	if err := os.WriteFile(path, make([]byte, rateEventMaxBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	rateEvents.append(rateEvent{Model: "m", Body: "x"})
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != rateEventMaxBytes {
		t.Fatalf("old file must rotate to .1: %v", err)
	}
	if evs := readRateEvents(t, path); len(evs) != 1 {
		t.Fatalf("new file must hold only the new event: %+v", evs)
	}
}

func TestTruncateRunesKeepsUTF8(t *testing.T) {
	if got := truncateRunes("将在重置", 2); got != "将在…" {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes("ok", 5); got != "ok" {
		t.Fatalf("got %q", got)
	}
}
