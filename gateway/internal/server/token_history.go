package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Hourly aggregates only: no prompts, responses, keys or account identifiers.
// ponytail: retain 400 days / 200000 model-hour buckets; use a database if this ceiling becomes insufficient.
type TokenBucket struct {
	Hour     int64  `json:"hour"`
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Observed int64  `json:"observed"`
	Input    int64  `json:"input"`
	Output   int64  `json:"output"`
}
type tokenHistory struct {
	mu      sync.Mutex
	path    string
	since   time.Time
	rows    map[string]TokenBucket
	dirty   bool
	warning string
	blocked bool
	dropped int64
}

var history = &tokenHistory{since: time.Now(), rows: map[string]TokenBucket{}}

const historyLimit = 200000

func (h *tokenHistory) prune(now time.Time) {
	cutoff := now.AddDate(0, 0, -400).Unix()
	for k, b := range h.rows {
		if b.Hour < cutoff {
			delete(h.rows, k)
			h.dirty = true
		}
	}
}
func (h *tokenHistory) record(s *chatStat, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hour := now.UTC().Truncate(time.Hour).Unix()
	model := s.model
	if len(model) > 256 {
		model = "[oversized model ID]"
	}
	if model == "" {
		model = "-"
	}
	key := fmt.Sprintf("%d/%s", hour, model)
	b, exists := h.rows[key]
	if !exists && len(h.rows) >= historyLimit {
		h.prune(now)
		if len(h.rows) >= historyLimit {
			h.dropped++
			h.dirty = true
			return
		}
	}
	b.Hour = hour
	b.Model = model
	b.Requests++
	if s.hasUsage {
		b.Observed++
		if s.prompt > 0 {
			b.Input += int64(s.prompt)
		}
		if s.toks > 0 {
			b.Output += int64(s.toks)
		}
	}
	h.rows[key] = b
	h.dirty = true
}
func (h *tokenHistory) load() error {
	f, err := os.Open(h.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var data struct {
		Since   time.Time     `json:"since"`
		Rows    []TokenBucket `json:"rows"`
		Dropped int64         `json:"dropped,omitempty"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 128<<20))
	if err = decoder.Decode(&data); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected data after history")
	}
	if data.Since.IsZero() || data.Dropped < 0 || len(data.Rows) > historyLimit {
		return fmt.Errorf("invalid history")
	}
	for _, b := range data.Rows {
		if b.Hour <= 0 || len(b.Model) > 256 || b.Requests < 0 || b.Observed < 0 || b.Observed > b.Requests || b.Input < 0 || b.Output < 0 {
			return fmt.Errorf("invalid history row")
		}
	}
	h.since = data.Since
	h.dropped = data.Dropped
	for _, b := range data.Rows {
		h.rows[fmt.Sprintf("%d/%s", b.Hour, b.Model)] = b
	}
	h.prune(time.Now())
	return nil
}
func (h *tokenHistory) flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.dirty || h.path == "" || h.blocked {
		return
	}
	h.prune(time.Now())
	rows := make([]TokenBucket, 0, len(h.rows))
	for _, b := range h.rows {
		rows = append(rows, b)
	}
	raw, err := json.Marshal(struct {
		Since   time.Time     `json:"since"`
		Rows    []TokenBucket `json:"rows"`
		Dropped int64         `json:"dropped,omitempty"`
	}{h.since, rows, h.dropped})
	if err == nil {
		err = writeTokenHistory(h.path, raw)
	}
	if err != nil {
		h.warning = "历史统计保存失败；当前数据仅在内存中"
		log.Printf("WARN: token history save failed: %v", err)
		return
	}
	h.dirty = false
	if h.warning == "历史统计保存失败；当前数据仅在内存中" {
		h.warning = ""
	}
}
func writeTokenHistory(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".token-history-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Dirty-only, once per minute plus clean shutdown; never write once per request.
func StartTokenHistory(stateFile string) func() {
	if stateFile == "" {
		history.warning = "未配置持久化路径，重启后历史统计会清空"
		return func() {}
	}
	history.path = filepath.Join(filepath.Dir(stateFile), "token-history.json")
	if err := history.load(); err != nil {
		history.blocked = true
		history.warning = "历史统计文件无法读取；原文件已保留，当前记录仅在内存中"
		log.Printf("WARN: token history load failed: %v", err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				history.flush()
			case <-stop:
				history.flush()
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}
func (h *Handler) tokenHistory(w http.ResponseWriter, r *http.Request) {
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		http.Error(w, "invalid from", 400)
		return
	}
	to, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || !to.After(from) || to.Sub(from) > 367*24*time.Hour {
		http.Error(w, "invalid range", 400)
		return
	}
	history.mu.Lock()
	since, warning := history.since, history.warning
	if history.dropped > 0 {
		warning += fmt.Sprintf(" 历史容量曾达上限，累计 %d 次请求未计入", history.dropped)
	}
	rows := make([]TokenBucket, 0)
	for _, b := range history.rows {
		if b.Hour >= from.Unix() && b.Hour < to.Unix() {
			rows = append(rows, b)
		}
	}
	history.mu.Unlock() // Network writes must not hold the request accounting lock.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Hour == rows[j].Hour {
			return rows[i].Model < rows[j].Model
		}
		return rows[i].Hour < rows[j].Hour
	})
	writeJSON(w, 200, map[string]any{"since": since, "rows": rows, "warning": warning, "retention_days": 400})
}
