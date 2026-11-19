package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEndpointRouteConfigValidation(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty name", `[{name: "", path: /one}]`},
		{"duplicate name", `[{name: same, path: /one}, {name: same, path: /two}]`},
		{"relative path", `[{name: one, path: one}]`},
		{"trailing slash", `[{name: one, path: /one/}]`},
		{"root path", `[{name: one, path: /}]`},
		{"duplicate path", `[{name: one, path: /one}, {name: two, path: /one}]`},
		{"relative rewrite", `[{name: one, path: /one, rewrite-to: v1}]`},
		{"trailing rewrite", `[{name: one, path: /one, rewrite-to: /v1/}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte("routes: " + tc.body + "\n")
			if _, errParse := ParseConfigBytes(body); errParse == nil {
				t.Fatal("ParseConfigBytes accepted invalid route")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, body, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if _, errLoad := LoadConfig(path); errLoad == nil {
				t.Fatal("LoadConfig accepted invalid route")
			}
		})
	}
}

func TestEndpointRouteConfigV8RoundTrip(t *testing.T) {
	body := []byte("config-version: 8\nroutes:\n  - name: cyber\n    path: /cyber\n    rewrite-to: /v1\n    providers: [' CODEX ', 'Claude']\n    credentials: [cyber.json]\nrequests:\n  payload:\n    override:\n      - models: [{name: '*', route: cyber}]\n        params: {temperature: 0.2}\n")
	cfg, errParse := ParseConfigBytes(body)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if got := strings.Join(cfg.Routes[0].Providers, ","); got != "codex,claude" {
		t.Fatalf("providers = %q", got)
	}
	if cfg.Payload.Override[0].Models[0].Route != "cyber" {
		t.Fatal("payload route lost")
	}
	normalized, _, errNormalize := NormalizeConfigLayout(body, true)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	var decoded Config
	if errDecode := yaml.Unmarshal(normalized, &decoded); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(decoded.Routes) != 1 || decoded.Routes[0].Credentials[0] != "cyber.json" {
		t.Fatalf("routes lost in v8 layout: %s", normalized)
	}
	encoded, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	roundTrip, errRoundTrip := ParseConfigBytes(encoded)
	if errRoundTrip != nil || len(roundTrip.Routes) != 1 {
		t.Fatalf("round trip: %v, %s", errRoundTrip, encoded)
	}
	clone := cfg.CloneForRuntime()
	cfg.Routes[0].Providers[0] = "changed"
	if clone.Routes[0].Providers[0] != "codex" {
		t.Fatal("runtime clone shares route selectors")
	}
}
