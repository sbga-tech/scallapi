package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEndpointRoutePayloadRules(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "*", Route: "cyber"}}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:     []config.PayloadRule{{Models: models, Params: map[string]any{"defaulted": true}}},
		DefaultRaw:  []config.PayloadRule{{Models: models, Params: map[string]any{"default_raw": `{"enabled":true}`}}},
		Override:    []config.PayloadRule{{Models: models, Params: map[string]any{"value": "configured"}}},
		OverrideRaw: []config.PayloadRule{{Models: models, Params: map[string]any{"override_raw": `[1,2]`}}},
		Replace:     []config.PayloadReplaceRule{{Models: models, Target: config.PayloadReplaceTarget{Paths: []string{"text"}}, Replacements: []config.PayloadReplacement{{"before": "after"}}}},
		Filter:      []config.PayloadFilterRule{{Models: models, Params: []string{"removed"}}},
	}}
	body := []byte(`{"model":"test","value":"original","text":"before","removed":true}`)
	for _, route := range []string{"cyber", "other", ""} {
		t.Run("route="+route, func(t *testing.T) {
			opts := coreexecutor.Options{}
			if route != "" {
				opts.Metadata = map[string]any{coreexecutor.RouteMetadataKey: route}
			}
			finalize := NewPayloadFinalizer(cfg, "codex", "test", "codex", "", body, coreexecutor.Request{Model: "test", Payload: body}, opts)
			for _, docMode := range []bool{false, true} {
				var out []byte
				if docMode {
					doc := ojson.NewDoc(body)
					finalize.FinalizeDoc(doc)
					out = doc.Bytes()
				} else {
					out = finalize(body)
				}
				if route != "cyber" {
					if string(out) != string(body) {
						t.Fatalf("nonmatching route changed payload: %s", out)
					}
					continue
				}
				if !ojson.GetBytes(out, "defaulted").Bool() || !ojson.GetBytes(out, "default_raw.enabled").Bool() || ojson.GetBytes(out, "value").String() != "configured" || len(ojson.GetBytes(out, "override_raw").Array()) != 2 || ojson.GetBytes(out, "text").String() != "after" || ojson.GetBytes(out, "removed").Exists() {
					t.Fatalf("route rule not applied: %s", out)
				}
			}
		})
	}
}

func TestEndpointRoutePayloadEntryPoints(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "test", Route: "cyber"}}, Params: map[string]any{"value": "configured"}}}}}
	body := []byte(`{"model":"test","value":"original"}`)
	applyDoc := func(apply func(*ojson.Doc)) []byte {
		doc := ojson.NewDoc(body)
		apply(doc)
		return doc.Bytes()
	}
	for _, route := range []string{"cyber", "other", ""} {
		for _, entry := range []struct {
			name  string
			apply func() []byte
		}{
			{"root", func() []byte {
				return ApplyPayloadConfigWithRoot(cfg, "test", "codex", "", body, nil, "", "/v1/responses", route)
			}},
			{"root-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithRootDoc(cfg, "test", "codex", "", doc, nil, "", "/v1/responses", route)
				})
			}},
			{"request", func() []byte {
				return ApplyPayloadConfigWithRequest(cfg, "test", "codex", "", "", body, nil, "", "/v1/responses", route, nil)
			}},
			{"request-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithRequestDoc(cfg, "test", "codex", "", "", doc, nil, "", "/v1/responses", route, nil)
				})
			}},
			{"executor", func() []byte {
				return ApplyPayloadConfigWithRequestForExecutor(cfg, "codex", "test", "codex", "", "", body, nil, "", "/v1/responses", route, nil)
			}},
			{"executor-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithRequestForExecutorDoc(cfg, "codex", "test", "codex", "", "", doc, nil, "", "/v1/responses", route, nil)
				})
			}},
			{"tracked", func() []byte {
				out, _ := ApplyPayloadConfigWithTrackedPaths(cfg, "test", "codex", "", "", body, nil, "", "/v1/responses", route, nil, "value")
				return out
			}},
			{"tracked-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithTrackedPathsDoc(cfg, "test", "codex", "", "", doc, nil, "", "/v1/responses", route, nil, "value")
				})
			}},
			{"tracked-executor", func() []byte {
				out, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, "codex", "test", "codex", "", "", body, nil, "", "/v1/responses", route, nil, "value")
				return out
			}},
			{"tracked-executor-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, "codex", "test", "codex", "", "", doc, nil, "", "/v1/responses", route, nil, "value")
				})
			}},
			{"single-tracked", func() []byte {
				out, _ := ApplyPayloadConfigWithRequestTracked(cfg, "test", "codex", "", "", body, nil, "", "/v1/responses", route, nil, "value")
				return out
			}},
			{"single-tracked-doc", func() []byte {
				return applyDoc(func(doc *ojson.Doc) {
					ApplyPayloadConfigWithRequestTrackedDoc(cfg, "test", "codex", "", "", doc, nil, "", "/v1/responses", route, nil, "value")
				})
			}},
		} {
			t.Run(entry.name+"/route="+route, func(t *testing.T) {
				out := entry.apply()
				want := "original"
				if route == "cyber" {
					want = "configured"
				}
				if got := ojson.GetBytes(out, "value").String(); got != want {
					t.Fatalf("value = %q, want %q; payload=%s", got, want, out)
				}
			})
		}
	}
}

func TestPayloadRoute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{"missing", nil, ""},
		{"nil", map[string]any{coreexecutor.RouteMetadataKey: nil}, ""},
		{"string", map[string]any{coreexecutor.RouteMetadataKey: " cyber "}, "cyber"},
		{"bytes", map[string]any{coreexecutor.RouteMetadataKey: []byte(" cyber ")}, "cyber"},
		{"invalid", map[string]any{coreexecutor.RouteMetadataKey: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PayloadRoute(coreexecutor.Options{Metadata: tc.metadata}); got != tc.want {
				t.Fatalf("PayloadRoute = %q, want %q", got, tc.want)
			}
		})
	}
}
