package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
)

func TestStatsDesktopScopeAndServerCompatibility(t *testing.T) {
	for _, desktop := range []bool{true, false} {
		name := "server"
		if desktop {
			name = "desktop"
		}
		t.Run(name, func(t *testing.T) {
			const query = "range=7d&from=2026-09-01&to=2026-09-26&interval=day&model=glm-5.2"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/stats" || (desktop && r.URL.RawQuery != "") {
					t.Errorf("unexpected gateway request %s", r.URL.RequestURI())
				}
				if !desktop {
					for key, value := range map[string]string{"range": "7d", "from": "2026-09-01", "to": "2026-09-26", "interval": "day", "model": "glm-5.2"} {
						if r.URL.Query().Get(key) != value {
							t.Errorf("server must still forward %s=%s", key, value)
						}
					}
				}
				io.WriteString(w, `{"enabled":true,"since":"2026-09-26T00:00:00Z","now":"2026-09-26T00:00:30Z","uptime_sec":30,"total":{"requests":2,"usage_observed_requests":1,"credit_observed_requests":0},"models":[{"model":"glm-5.2","requests":2,"usage_observed_requests":1,"credit_observed_requests":0}],"series_buckets":7,"range":{"interval":"day","points":[]}}`)
			}))
			defer srv.Close()
			s := newPolicyServer(desktop)
			s.cfg.PricingFile = ""
			s.svc = ops.New(s.cfg, nil, gateway.New(srv.URL, func() string { return "synthetic" }, time.Second), nil)
			token, ok, _ := s.sessions.Create("admin", "secret123", s.cfg, "127.0.0.1")
			if !ok {
				t.Fatal("create session")
			}
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/stats?"+query, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("stats status=%d err=%v body=%s", rec.Code, err, rec.Body)
			}
			var st map[string]json.RawMessage
			if err := json.Unmarshal(body["stats"], &st); err != nil {
				t.Fatal(err)
			}
			if string(st["since"]) != `"2026-09-26T00:00:00Z"` || string(st["uptime_sec"]) != "30" {
				t.Fatalf("period lost: %s", body["stats"])
			}
			if desktop {
				if string(body["scope"]) != `"current_run"` || st["range"] != nil || st["series_buckets"] != nil {
					t.Fatalf("desktop must explicitly expose only current run: %s", rec.Body)
				}
				for _, key := range []string{"costs", "total", "pricing", "priced", "unpriced", "mode"} {
					if body[key] != nil {
						t.Errorf("desktop must not expose estimated pricing field %s", key)
					}
				}
			} else if body["scope"] != nil || body["costs"] == nil || body["pricing"] == nil || st["range"] == nil || st["series_buckets"] == nil {
				t.Fatalf("server stats compatibility lost: %s", rec.Body)
			}
			var decoded gateway.Stats
			if err := json.Unmarshal(body["stats"], &decoded); err != nil {
				t.Fatal(err)
			}
			for _, row := range []gateway.ModelStat{decoded.Total, decoded.Models[0]} {
				if row.UsageObservedRequests == nil || *row.UsageObservedRequests != 1 || row.CreditObservedRequests == nil || *row.CreditObservedRequests != 0 {
					t.Fatalf("observation counts lost: %+v", row)
				}
			}
		})
	}
}
