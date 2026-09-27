package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestModelsPreservesOptionalMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unexpected model request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"cn:synthetic","object":"model","created":1,"owned_by":"workbuddy","name":"合成推理模型","description":"适合复杂任务","reasoning_supported_efforts":["low","medium","high"],"reasoning_default_effort":"medium","context_length":192000,"max_output_tokens":64000},{"id":"cn:minimal","object":"model","created":2,"owned_by":"workbuddy","context_length":128000}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, func() string { return "synthetic-key" }, time.Second)
	defer c.http.CloseIdleConnections()
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("Models: %v, count=%d", err, len(models))
	}
	full := models[0]
	if full.ID != "cn:synthetic" || full.Name != "合成推理模型" || full.Description != "适合复杂任务" ||
		!reflect.DeepEqual(full.ReasoningSupportedEfforts, []string{"low", "medium", "high"}) ||
		full.ReasoningDefaultEffort != "medium" || full.ContextLength != 192000 || full.MaxOutput != 64000 {
		t.Fatalf("model metadata lost: %+v", full)
	}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip []map[string]any
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "description", "reasoning_supported_efforts", "reasoning_default_effort", "context_length", "max_output_tokens"} {
		if _, ok := roundTrip[0][key]; !ok {
			t.Fatalf("full model lost %s", key)
		}
	}
	if roundTrip[1]["id"] != "cn:minimal" {
		t.Fatalf("minimal model ID lost: %v", roundTrip[1])
	}
	for _, key := range []string{"name", "description", "reasoning_supported_efforts", "reasoning_default_effort"} {
		if _, ok := roundTrip[1][key]; ok {
			t.Fatalf("missing metadata was invented: %s", key)
		}
	}
}
