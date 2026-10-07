package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenHistoryPersistenceAndBoundaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	h := &tokenHistory{path: filepath.Join(t.TempDir(), "history.json"), since: now, rows: map[string]TokenBucket{}}
	h.record(&chatStat{model: "cn:test", hasUsage: true, prompt: 100, toks: 20}, now)
	h.record(&chatStat{model: "cn:test", toks: -1}, now)
	h.record(&chatStat{model: "global:test", hasUsage: true, prompt: 5, toks: 0}, now.Add(time.Hour))
	h.record(&chatStat{model: "expired", hasUsage: true, prompt: 999}, now.AddDate(0, 0, -401))
	h.flush()
	loaded := &tokenHistory{path: h.path, rows: map[string]TokenBucket{}}
	if err := loaded.load(); err != nil {
		t.Fatal(err)
	}
	if len(loaded.rows) != 2 {
		t.Fatalf("retention/reload: %+v", loaded.rows)
	}
	var first TokenBucket
	for _, v := range loaded.rows {
		if v.Model == "cn:test" {
			first = v
		}
	}
	if first.Requests != 2 || first.Observed != 1 || first.Input != 100 || first.Output != 20 {
		t.Fatalf("usage: %+v", first)
	}
	before, _ := os.Stat(h.path)
	h.flush()
	after, _ := os.Stat(h.path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("clean flush wrote to disk")
	}
	old := history
	history = loaded
	defer func() { history = old }()
	ResetMetrics()
	if len(history.rows) != 2 {
		t.Fatal("current-run reset erased history")
	}
	handler := &Handler{}
	for _, tc := range []struct {
		query string
		code  int
		model string
	}{
		{"from=" + now.Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339), 200, "cn:test"},
		{"from=bad&to=bad", 400, ""},
		{"from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", 400, ""},
	} {
		rec := httptest.NewRecorder()
		handler.tokenHistory(rec, httptest.NewRequest("GET", "/v1/token-history?"+tc.query, nil))
		if rec.Code != tc.code {
			t.Fatalf("status %d", rec.Code)
		}
		if tc.code == 200 {
			var body struct {
				Rows []TokenBucket `json:"rows"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Rows) != 1 || body.Rows[0].Model != tc.model {
				t.Fatalf("range leaked: %s", rec.Body)
			}
		}
	}
}

func TestTokenHistoryDesktopAccess(t *testing.T) {
	h := NewHandler(Config{DesktopMode: true, DesktopLAN: true, APIKey: "synthetic"})
	for _, tc := range []struct {
		peer, key string
		code      int
	}{{"127.0.0.1:34567", "synthetic", 200}, {"127.0.0.1:34567", "", 401}, {"192.168.1.2:34567", "synthetic", 403}} {
		r := httptest.NewRequest("GET", "/v1/token-history?from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z", nil)
		r.RemoteAddr = tc.peer
		if tc.key != "" {
			r.Header.Set("Authorization", "Bearer "+tc.key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s got %d want %d", tc.peer, w.Code, tc.code)
		}
	}
}

// A response writer can stall; accounting must remain independent of it.
type tokenRecordingWriter struct {
	*httptest.ResponseRecorder
	store *tokenHistory
}

func (w tokenRecordingWriter) Write(p []byte) (int, error) {
	w.store.record(&chatStat{model: "during-response"}, time.Now())
	return w.ResponseRecorder.Write(p)
}
func TestTokenHistoryReadUnlocksAndPreservesCorruption(t *testing.T) {
	h := &tokenHistory{path: filepath.Join(t.TempDir(), "history.json"), since: time.Now(), rows: map[string]TokenBucket{}, dropped: 3, dirty: true}
	h.flush()
	loaded := &tokenHistory{path: h.path, rows: map[string]TokenBucket{}}
	if err := loaded.load(); err != nil || loaded.dropped != 3 {
		t.Fatalf("dropped count lost: %v", err)
	}
	old := history
	history = loaded
	defer func() { history = old }()
	w := tokenRecordingWriter{httptest.NewRecorder(), loaded}
	(&Handler{}).tokenHistory(w, httptest.NewRequest("GET", "/v1/token-history?from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z", nil))
	if w.Code != 200 || len(loaded.rows) != 1 {
		t.Fatal("accounting blocked by response")
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append(raw, []byte(" trailing corruption")...)
	if err := os.WriteFile(h.path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := loaded.load(); err == nil {
		t.Fatal("accepted corrupt trailing data")
	}
	after, _ := os.ReadFile(h.path)
	if string(after) != string(corrupt) {
		t.Fatal("changed corrupt file")
	}
}
