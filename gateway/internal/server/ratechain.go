// ratechain.go 429 长重置连锁保护 + 429 原文事件落盘。
//
// 背景（2026-10-24 实测）：9 个账号在 90 秒内接连收到「将在 2 小时后重置」的 429，
// 全池冷却 2h。上游限的是共享维度（同出口 IP / 同模型总配额）时，换号毫无意义：
// 单次客户端请求按 MaxRotate 轮转，每撞一个号就把它冷却到重置时刻——两个 agent
// 各重试几次就把整个池拖进冷却。
//
// 判定（与 wafip.go 同构，短窗多号）：按**模型**分桶（限流若只针对某模型，其他
// 模型的请求不受影响），rateChainWindow 窗内 ≥ rateChainThreshold 个**不同** UID
// 对同一模型返回长重置 429（重置时刻距今 ≥ rateChainLongReset）→ 激活该模型的
// 连锁保护至 now+rateChainWindow。激活期内请求一旦撞上长重置 429 立即终止轮转、
// 回 429，不再拖下一个号下水。账号级冷却照常记账（applyErrorPolicy 不变），本状态
// 只改变「是否继续轮转」。短重置 / 无重置时间的 429 是普通频控，不参与判定。
//
// 429 原文落盘：桌面外壳出于隐私丢弃网关 stdout，排查限流维度（6004 模型级 /
// 账号级 / 共享）只能靠上游原文。每次 429 追加一行 JSON 到数据目录
// ratelimit-events.jsonl（与 state.json 同目录，单文件超 rateEventMaxBytes 轮转为 .1）。
// 只记上游错误体（code/msg/requestId），不记请求体与凭据。
package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/privatefs"
	"workbuddy2api/internal/upstream"
)

// rateChainWindow 判定滑动窗 + 激活时长。var 仅供测试注入短窗。
var rateChainWindow = 5 * time.Minute

// rateChainLongReset 「长重置」门槛：重置时刻距今不短于该值才计入连锁判定。
// 普通频控通常几秒到几分钟自愈，不应触发止损。
const rateChainLongReset = 10 * time.Minute

// rateChainThreshold 窗内不同 UID 数达到该值激活（单号反复 429 永不触发）。
const rateChainThreshold = 2

// rateChainGate 按模型分桶的连锁保护状态机（Handler 内嵌，零值可用）。
type rateChainGate struct {
	mu    sync.Mutex
	hits  map[string]map[string]time.Time // model → uid → 最近一次长重置 429 时刻
	until map[string]time.Time            // model → 激活截止
}

// noteLongRate 记一次某账号对 model 的长重置 429，返回记账后该模型连锁保护是否激活
// （调用方据此终止轮转）。激活期内新命中不续期（窗口到期自然解除）。
func (g *rateChainGate) noteLongRate(model, uid string) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until[model]) {
		return true
	}
	if g.hits == nil {
		g.hits = map[string]map[string]time.Time{}
		g.until = map[string]time.Time{}
	}
	bucket := g.hits[model]
	if bucket == nil {
		bucket = map[string]time.Time{}
		g.hits[model] = bucket
	}
	bucket[uid] = now
	for u, t := range bucket {
		if now.Sub(t) > rateChainWindow {
			delete(bucket, u)
		}
	}
	if len(bucket) >= rateChainThreshold {
		g.until[model] = now.Add(rateChainWindow)
		log.Printf("WARN: [server] 429 chain guard: %d accounts got long-reset 429 on model=%s within %s, rotation stops until %s", len(bucket), model, rateChainWindow, g.until[model].Format(time.RFC3339))
		delete(g.hits, model)
		return true
	}
	return false
}

// active 报告该模型连锁保护是否激活。
func (g *rateChainGate) active(model string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.until[model])
}

// softRateResetAt 返回 429 的上游重置时刻：body 重置文案优先，其次 Retry-After 头
// （与 applyErrorPolicy 同优先级）。都没有 → 零值。
func softRateResetAt(body string, uerr *upstream.Error) time.Time {
	if t, ok := upstream.ParseRateReset(body); ok {
		return t
	}
	if uerr != nil && uerr.RetryAfter > 0 {
		return time.Now().Add(uerr.RetryAfter)
	}
	return time.Time{}
}

// noteSoftRate 处理一次 ErrSoftRate：落盘 429 原文事件，并喂连锁保护。
// 返回 true 表示应终止轮转（该模型连锁保护已激活且本次是长重置 429）。
func (h *Handler) noteSoftRate(acct *auth.Auth, model string, status int, body string, uerr *upstream.Error) bool {
	resetAt := softRateResetAt(body, uerr)
	long := !resetAt.IsZero() && time.Until(resetAt) >= rateChainLongReset
	stop := false
	if long {
		stop = h.rateChain.noteLongRate(model, acct.UID)
	}
	ev := rateEvent{
		Time:       time.Now().Format(time.RFC3339),
		Account:    logfmt.Label(acct.UID, acct.Nickname),
		Model:      model,
		Status:     status,
		ModelLevel: upstream.IsModelRateLimit(body),
		LongReset:  long,
		ChainStop:  stop,
		Body:       truncateRunes(strings.TrimSpace(body), rateEventBodyMax),
	}
	if !resetAt.IsZero() {
		ev.ResetAt = resetAt.Format(time.RFC3339)
	}
	log.Printf("WARN: [server] 429 acct=%s model=%s model_level=%t reset_at=%s chain_stop=%t body=%s", ev.Account, model, ev.ModelLevel, ev.ResetAt, stop, ev.Body)
	rateEvents.append(ev)
	return stop
}

// rateEvent ratelimit-events.jsonl 单行。
type rateEvent struct {
	Time       string `json:"time"`
	Account    string `json:"account"`
	Model      string `json:"model"`
	Status     int    `json:"status"`
	ModelLevel bool   `json:"model_level"` // 上游 6004 模型级限流
	ResetAt    string `json:"reset_at,omitempty"`
	LongReset  bool   `json:"long_reset"`
	ChainStop  bool   `json:"chain_stop"` // 本次触发连锁保护、终止了轮转
	Body       string `json:"body"`       // 上游错误体原文（截断）
}

const (
	rateEventBodyMax  = 2000
	rateEventMaxBytes = 1 << 20
)

// rateEventLog 429 事件文件（包级单例，与 token history 同风格）。path 为空 = 不落盘。
type rateEventLog struct {
	mu   sync.Mutex
	path string
}

var rateEvents = &rateEventLog{}

// StartRateEventLog 接线 429 事件文件：与 state.json 同目录的 ratelimit-events.jsonl。
// stateFile 为空（未配置持久化）时不落盘，只打 stdout 日志。
func StartRateEventLog(stateFile string) {
	rateEvents.mu.Lock()
	defer rateEvents.mu.Unlock()
	if stateFile == "" {
		rateEvents.path = ""
		return
	}
	rateEvents.path = filepath.Join(filepath.Dir(stateFile), "ratelimit-events.jsonl")
}

// append 追加一行；超过 rateEventMaxBytes 先把旧文件轮转为 .1（覆盖上一份）。
// 任何 IO 失败只打 WARN，不影响请求主流程。
func (l *rateEventLog) append(ev rateEvent) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return
	}
	if fi, err := os.Stat(l.path); err == nil && fi.Size()+int64(len(raw))+1 > rateEventMaxBytes {
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			log.Printf("WARN: [server] ratelimit events rotate failed: %v", err)
		}
	}
	_, statErr := os.Stat(l.path)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("WARN: [server] ratelimit events open failed: %v", err)
		return
	}
	defer f.Close()
	if os.IsNotExist(statErr) {
		_ = privatefs.SecurePrivateACL(l.path) // Windows 收紧 ACL；其他平台空操作
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		log.Printf("WARN: [server] ratelimit events write failed: %v", err)
	}
}

// truncateRunes 按字符（非字节）截断，避免把中文原文切成半个 UTF-8 序列。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
