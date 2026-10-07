package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatsPreservesObservationAvailability(t *testing.T) {
	for _, fields := range []string{"", `,"usage_observed_requests":0,"credit_observed_requests":0`, `,"usage_observed_requests":3,"credit_observed_requests":1`} {
		t.Run(fields, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != "/v1/stats" {
					t.Errorf("unexpected stats request %s", r.URL.RequestURI())
				}
				row := `{"requests":3,"total_tokens":0` + fields + `}`
				fmt.Fprintf(w, `{"enabled":true,"since":"2026-09-26T00:00:00Z","now":"2026-09-26T00:00:30Z","uptime_sec":30,"total":%s,"models":[%s]}`, row, row)
			}))
			defer srv.Close()
			c := New(srv.URL, func() string { return "synthetic" }, time.Second)
			defer c.http.CloseIdleConnections()
			st, err := c.Stats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Models) != 1 || st.UptimeSec != 30 || !st.Since.Equal(time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("stats period or model lost: %+v", st)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(`{"requests":3,"total_tokens":0`+fields+`}`), &want); err != nil {
				t.Fatal(err)
			}
			for _, row := range []ModelStat{st.Total, st.Models[0]} {
				data, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"usage_observed_requests", "credit_observed_requests"} {
					value, present := got[key]
					expected, expectedPresent := want[key]
					if present != expectedPresent || value != expected {
						t.Fatalf("%s=%v present=%t want %v present=%t", key, value, present, expected, expectedPresent)
					}
				}
			}
		})
	}
}
