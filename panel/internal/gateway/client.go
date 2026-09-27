// Package gateway 访问 workbuddy2api 网关自身的 OpenAI 兼容 / 管理接口。
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ServicesStatusList 是网关 /status 返回的账号条目（字段与 pool.Status 对齐）。
type AccountStatus struct {
	UID             string    `json:"uid"`
	Nickname        string    `json:"nickname,omitempty"`
	Credits         int64     `json:"credits"`
	Cooling         bool      `json:"cooling"`
	CoolKind        string    `json:"cool_kind,omitempty"`
	CoolRemaining   int64     `json:"cool_remaining_sec,omitempty"`
	Until           time.Time `json:"until,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	SoftStreak      int       `json:"soft_streak,omitempty"`
	Disabled        bool      `json:"disabled"`
	ManualDisabled  bool      `json:"manual_disabled"`
	ManualReason    string    `json:"manual_reason,omitempty"`
	SuccessCount    int64     `json:"success_count,omitempty"`
	ErrTotal        int64     `json:"err_total,omitempty"`
	LastSuccessTime time.Time `json:"last_success,omitempty"`
	LastErrTime     time.Time `json:"last_err,omitempty"`
	InFlight        int       `json:"in_flight"`
	BreakerFails    int       `json:"breaker_fails"`
	BreakerUntil    time.Time `json:"breaker_until,omitempty"`
}

// Status 网关 /status 响应。
type Status struct {
	Accounts       []AccountStatus `json:"accounts"`
	Total          int             `json:"total"`
	Healthy        int             `json:"healthy"`
	Cooling        int             `json:"cooling"`
	Disabled       int             `json:"disabled"`
	InFlightFull   int             `json:"in_flight_full"`
	StickySessions int             `json:"sticky_sessions"`
	RedisMode      string          `json:"redis_mode"`
}

// Health 网关 /healthz 响应。
type Health struct {
	Healthy int    `json:"healthy"`
	Total   int    `json:"total"`
	Service string `json:"service"`
}

// Model OpenAI 模型条目。
type Model struct {
	ID                        string   `json:"id"`
	Object                    string   `json:"object"`
	Created                   int64    `json:"created"`
	OwnedBy                   string   `json:"owned_by"`
	Name                      string   `json:"name,omitempty"`
	Description               string   `json:"description,omitempty"`
	ReasoningSupportedEfforts []string `json:"reasoning_supported_efforts,omitempty"`
	ReasoningDefaultEffort    string   `json:"reasoning_default_effort,omitempty"`
	ContextLength             int64    `json:"context_length"`
	MaxOutput                 int64    `json:"max_output_tokens"`
}

// Client 网关客户端。每次请求都携带当前 api_key，因此支持运行期改配置。
type Client struct {
	mu      sync.RWMutex
	baseURL string
	apiKey  func() string
	http    *http.Client
}

// New 构建客户端；apiKey 用函数注入以便配置热更新。
func New(baseURL string, apiKey func() string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// BaseURL 返回当前网关地址。
func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// SetBaseURL 更新网关地址（配置热更新用）。
func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(u, "/")
}

// SetConnection publishes one desktop URL/key pair. Web callers that only use
// New or SetBaseURL keep their original live API-key callback.
func (c *Client) SetConnection(u, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL, c.apiKey = strings.TrimRight(u, "/"), func() string { return key }
}

func (c *Client) connection() (string, string) {
	c.mu.RLock()
	base, key := c.baseURL, c.apiKey
	c.mu.RUnlock()
	if key == nil {
		return base, ""
	}
	return base, key()
}

// do 发一次带鉴权的 GET 请求。
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	base, key := c.connection()
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// Health 探活网关。注意 /healthz 无鉴权，但需校验 service 字段以确认
// 「打到的确实是 workbuddy2api」，避免同端口其他服务造成的假成功。
func (c *Client) Health(ctx context.Context) (*Health, error) {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var h Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("网关 /healthz 响应无法解析（可能不是 workbuddy2api）: %w", err)
	}
	if h.Service != "workbuddy2api" {
		return nil, fmt.Errorf("端口上的服务不是 workbuddy2api（service=%q）", h.Service)
	}
	return &h, nil
}

// Status 拉取账号池状态。
func (c *Client) Status(ctx context.Context) (*Status, error) {
	resp, err := c.do(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("网关拒绝鉴权（401）：请检查 api_key 是否正确")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关 /status 返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("解析 /status 失败: %w", err)
	}
	return &st, nil
}

// Models 拉取模型列表（可能触发网关向上游拉取，耗时较长）。
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("网关拒绝鉴权（401）：请检查 api_key 是否正确")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关 /v1/models 返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	return out.Data, nil
}

// ChatResult 非流式聊天结果。Usage 保留缺字段与实际 0 的区别。
type ChatResult struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Model            string `json:"model"`
	FinishReason     string `json:"finish_reason,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	Usage            *Usage `json:"usage,omitempty"`
	Raw              string `json:"raw,omitempty"`
}

func (c *Client) Chat(ctx context.Context, body []byte) (*ChatResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("读取聊天响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", explainGatewayError(resp.StatusCode, raw))
	}
	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析聊天响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].FinishReason == "" {
		return nil, fmt.Errorf("连接中断：聊天响应缺少完成标记")
	}
	choice := parsed.Choices[0]
	out := &ChatResult{Model: parsed.Model, Content: choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent, FinishReason: choice.FinishReason,
		Usage: parsed.Usage, Raw: string(raw)}
	// 保留旧调用者的扁平字段；面板通过 Usage 判断实际观测值。
	if parsed.Usage != nil {
		if parsed.Usage.PromptTokens != nil {
			out.PromptTokens = *parsed.Usage.PromptTokens
		}
		if parsed.Usage.CompletionTokens != nil {
			out.CompletionTokens = *parsed.Usage.CompletionTokens
		}
		if parsed.Usage.TotalTokens != nil {
			out.TotalTokens = *parsed.Usage.TotalTokens
		}
	}
	return out, nil
}

type Delta struct {
	Content      string `json:"content,omitempty"`
	Reasoning    string `json:"reasoning,omitempty"`
	Done         bool   `json:"done,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Usage struct {
	PromptTokens     *int `json:"prompt_tokens,omitempty"`
	CompletionTokens *int `json:"completion_tokens,omitempty"`
	TotalTokens      *int `json:"total_tokens,omitempty"`
}

// ChatStream 只在明确结束后发 Done；传输、协议和上游错误均返回 error。
func (c *Client) ChatStream(ctx context.Context, body []byte, onDelta func(Delta) error) error {
	base, key := c.connection()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// 请求寿命由调用者 context 和网关上游超时控制。
	streamClient := &http.Client{Transport: c.http.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接网关失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("%s", explainGatewayError(resp.StatusCode, raw))
	}

	finishReason := ""
	consume := func(payload string) (bool, error) {
		if payload == "[DONE]" {
			return true, onDelta(Delta{Done: true, FinishReason: finishReason})
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage          `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return false, fmt.Errorf("连接中断：流式响应格式无效: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, fmt.Errorf("%s", explainGatewayError(http.StatusBadGateway, []byte(payload)))
		}
		d := Delta{Usage: chunk.Usage}
		if len(chunk.Choices) > 0 {
			d.Content = chunk.Choices[0].Delta.Content
			d.Reasoning = chunk.Choices[0].Delta.ReasoningContent
			d.FinishReason = chunk.Choices[0].FinishReason
			if d.FinishReason != "" {
				finishReason = d.FinishReason
			}
		}
		return false, onDelta(d)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var data []string
	for sc.Scan() {
		line := sc.Text() // Scanner 已处理 CRLF 和网络半行。
		if line == "" {
			if len(data) == 0 {
				continue
			}
			done, err := consume(strings.Join(data, "\n"))
			data = nil
			if done || err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("连接中断：读取流失败: %w", err)
	}
	if len(data) > 0 || finishReason == "" {
		return fmt.Errorf("连接中断：未收到完成标记")
	}
	return onDelta(Delta{Done: true, FinishReason: finishReason})
}

// explainGatewayError 把网关的 OpenAI 风格错误体翻译成人话。
func explainGatewayError(status int, raw []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		msg = env.Error.Message
	} else {
		msg = truncate(string(raw), 200)
	}
	switch status {
	case http.StatusUnauthorized:
		return "网关鉴权失败（401）：api_key 不正确"
	case http.StatusBadRequest:
		return "参数不合法（400）：" + msg
	case http.StatusNotFound:
		return "模型或接口不可用（404）：" + msg
	case http.StatusTooManyRequests:
		return "额度不足或请求限流（429）：" + msg
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return fmt.Sprintf("上游请求超时（%d）：%s", status, msg)
	case http.StatusServiceUnavailable:
		return "网关无可用账号（503）：" + msg
	case http.StatusBadGateway:
		return "上游网络或响应错误（502）：" + msg
	default:
		return fmt.Sprintf("网关返回 HTTP %d：%s", status, msg)
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------------------------------------------------------------------
// 请求统计（/v1/stats）
// ---------------------------------------------------------------------------

// ModelStat 单个模型的派生统计（与网关 metrics.Derived 对应）。
type ModelStat struct {
	Model string `json:"model"`

	Requests  int64 `json:"requests"`
	Success   int64 `json:"success"`
	Failed    int64 `json:"failed"`
	Streaming int64 `json:"streaming"`

	// nil 表示旧网关未提供观测次数，不能推断为零次观测。
	UsageObservedRequests  *int64 `json:"usage_observed_requests,omitempty"`
	CreditObservedRequests *int64 `json:"credit_observed_requests,omitempty"`

	// AvgTTFBMS 平均首字延迟（毫秒）。
	AvgTTFBMS float64 `json:"avg_ttfb_ms"`
	// AvgLatencyMS 平均端到端耗时（毫秒）。
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	// TokensPerSec 生成吞吐（输出 token / 生成秒数）。
	TokensPerSec float64 `json:"tokens_per_sec"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	CacheHitTokens   int64   `json:"cache_hit_tokens"`
	CacheMissTokens  int64   `json:"cache_miss_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"`

	Credit       float64 `json:"credit"`
	CreditPerReq float64 `json:"credit_per_req"`

	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// Stats 网关 /v1/stats 响应。
type Stats struct {
	Enabled   bool        `json:"enabled"`
	Message   string      `json:"message,omitempty"`
	Since     time.Time   `json:"since"`
	Now       time.Time   `json:"now"`
	UptimeSec int64       `json:"uptime_sec"`
	Total     ModelStat   `json:"total"`
	Models    []ModelStat `json:"models"`
	// SeriesBuckets 时间序列的桶数（判断数据可回溯范围）。
	SeriesBuckets int `json:"series_buckets"`
	// Range 时间维度查询结果（仅当请求带了 range/from/to/interval/model 时返回）。
	Range *RangeResult `json:"range,omitempty"`
}

// RangePoint 时间序列上的一个数据点。
type RangePoint struct {
	Key     string    `json:"key"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Stats   ModelStat `json:"stats"`
	Derived ModelStat `json:"derived"` // 派生指标复用同一结构（字段一致）
}

// RangeResult 时间范围聚合结果。
type RangeResult struct {
	Interval string       `json:"interval"`
	From     time.Time    `json:"from"`
	To       time.Time    `json:"to"`
	Points   []RangePoint `json:"points"`
	Total    ModelStat    `json:"total"`
	Models   []string     `json:"models"`
}

// StatsOptions 时间维度查询参数。
type StatsOptions struct {
	// Range 相对区间：today / yesterday / 7d / 30d / 90d / all
	Range string
	// From/To 绝对区间（RFC3339）；设置后优先于 Range。
	From string
	To   string
	// Interval 聚合粒度：hour / day / week
	Interval string
	// Model 只看单个模型（空 = 全部）
	Model string
}

// Stats 拉取按模型聚合的请求统计。
func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	return c.StatsRange(ctx, StatsOptions{})
}

// StatsRange 拉取统计（可选时间维度参数）。
func (c *Client) StatsRange(ctx context.Context, opt StatsOptions) (*Stats, error) {
	q := url.Values{}
	if opt.Range != "" {
		q.Set("range", opt.Range)
	}
	if opt.From != "" {
		q.Set("from", opt.From)
	}
	if opt.To != "" {
		q.Set("to", opt.To)
	}
	if opt.Interval != "" {
		q.Set("interval", opt.Interval)
	}
	if opt.Model != "" {
		q.Set("model", opt.Model)
	}
	path := "/v1/stats"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}

	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("网关拒绝鉴权（401）：请检查 api_key 是否正确")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关 /v1/stats 返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out Stats
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析统计失败: %w", err)
	}
	return &out, nil
}

// ResetStats 重置网关统计（清空累计，便于观察增量）。
func (c *Client) ResetStats(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, "/v1/stats/reset", []byte("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("重置统计失败（HTTP %d）: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return nil
}
