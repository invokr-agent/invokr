package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/invokr-agent/invokr/config"
	inference "github.com/rivt-ai/go-inference-router"
)

func TestModelOptionsWireMapping(t *testing.T) {
	for _, key := range []string{"INVOKR_N_PREDICT", "INVOKR_TEMPERATURE", "INVOKR_TOP_P"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name                   string
		values                 map[string]string
		cap, temperature, topP any
	}{
		{"inherit", nil, float64(16384), float64(1), nil},
		{"set", map[string]string{"max_tokens": "32768", "temperature": "0", "top_p": "0.8"}, float64(32768), float64(0), 0.8},
		{"omit", map[string]string{"max_tokens": "omit", "temperature": "omit"}, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer srv.Close()
			base := inference.Request{Model: "m", Messages: []inference.Message{inference.UserMessage("hello")}, MaxOutputTokens: 16384, Temperature: inference.Float(1)}
			req, err := ApplyModelOptions(base, config.AgentConfig{ModelOptions: map[string]map[string]string{"m": tc.values}})
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(srv.URL)
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := client.Completion(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{"max_completion_tokens": tc.cap, "max_tokens": nil, "temperature": tc.temperature, "top_p": tc.topP} {
				if !reflect.DeepEqual(body[key], want) {
					t.Fatalf("%s = %v, want %v; body=%v", key, body[key], want, body)
				}
			}
		})
	}
}

func TestModelOptionsEnvironmentAndIsolation(t *testing.T) {
	t.Setenv("INVOKR_N_PREDICT", "8192")
	t.Setenv("INVOKR_TEMPERATURE", "0.3")
	t.Setenv("INVOKR_TOP_P", "0.7")
	base := inference.Request{Model: "m", MaxOutputTokens: 8192, Temperature: inference.Float(0.3)}
	settings := config.AgentConfig{ModelOptions: map[string]map[string]string{"m": {"max_tokens": "32768", "temperature": "1.8", "top_p": "0.9"}}}
	req, err := ApplyModelOptions(base, settings)
	if err != nil || req.MaxOutputTokens != 8192 || *req.Temperature != 0.3 || *req.TopP != 0.7 {
		t.Fatalf("env precedence: %+v, %v", req, err)
	}
	settings.TemperatureDisabled = true
	req, err = ApplyModelOptions(base, settings)
	if err != nil || req.Temperature != nil {
		t.Fatalf("disabled temperature sent: %+v, %v", req, err)
	}
	for _, key := range []string{"INVOKR_N_PREDICT", "INVOKR_TEMPERATURE", "INVOKR_TOP_P"} {
		t.Setenv(key, "")
	}
	base.Model = "other"
	req, err = ApplyModelOptions(base, settings)
	if err != nil || req.MaxOutputTokens != 8192 || req.TopP != nil {
		t.Fatalf("model options leaked: %+v, %v", req, err)
	}
	settings.ModelOptions["other"] = map[string]string{"top_p": "NaN"}
	if _, err := ApplyModelOptions(base, settings); err == nil {
		t.Fatal("invalid request option accepted")
	}
}
