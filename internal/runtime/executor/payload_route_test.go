package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutorRoutePayloadRule(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "shared-model", Route: "cyber"}}, Params: map[string]any{"temperature": 0.73}}}}}
	for _, stream := range []bool{false, true} {
		for _, route := range []string{"cyber", "other", ""} {
			t.Run(map[bool]string{false: "execute/", true: "stream/"}[stream]+"route="+route, func(t *testing.T) {
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					captured <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"route-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"route-test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				executor := NewOpenAICompatExecutor("test-compat", cfg)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "test-compat", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL + "/v1"}}
				req := cliproxyexecutor.Request{Model: "shared-model", Payload: []byte(`{"model":"shared-model","messages":[{"role":"user","content":"hello"}],"temperature":0.1}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream}
				if route != "" {
					opts.Metadata = map[string]any{cliproxyexecutor.RouteMetadataKey: route}
				}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				body := <-captured
				want := 0.1
				if route == "cyber" {
					want = 0.73
				}
				if got := ojson.GetBytes(body, "temperature").Float(); got != want {
					t.Fatalf("temperature = %v, want %v; upstream body=%s", got, want, body)
				}
			})
		}
	}
}

func TestClaudeExecutorRoutePayloadRule(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "shared-model", Route: "cyber"}}, Params: map[string]any{"temperature": 0.73}}}}}
	for _, method := range []string{"Execute", "ExecuteStream"} {
		for _, route := range []string{"cyber", "other", ""} {
			t.Run(method+"/route="+route, func(t *testing.T) {
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "shared-model"}}
				if route != "" {
					opts.Metadata[cliproxyexecutor.RouteMetadataKey] = route
				}
				req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],"temperature":0.1}`)}
				body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(cfg), &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "test-key"}}, req, opts, method)
				temperature := ojson.GetBytes(body, "temperature")
				if route == "cyber" {
					if !temperature.Exists() || temperature.Float() != 0.73 {
						t.Fatalf("route override missing from prepared upstream body: %s", body)
					}
				} else if temperature.Exists() {
					t.Fatalf("nonmatching route restored a sampling parameter removed by Claude preparation: %s", body)
				}
			})
		}
	}
}
