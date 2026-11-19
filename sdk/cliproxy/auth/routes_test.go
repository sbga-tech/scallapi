package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEndpointRouteAuthEligibility(t *testing.T) {
	snapshot := routes.Compile([]routes.Config{
		{Name: "provider", Path: "/provider", Providers: []string{"codex"}},
		{Name: "file", Path: "/file", Providers: []string{"codex"}, AuthFiles: []string{"allowed.json"}},
		{Name: "compat", Path: "/compat", Providers: []string{"openai-compatible-acme"}},
		{Name: "original", Path: "/original", Providers: []string{"openai-compatibility"}},
	})
	fileAuth := &Auth{Provider: "codex", FileName: "allowed.json"}
	pathAuth := &Auth{Provider: "codex", Attributes: map[string]string{"path": "/auths/allowed.json"}}
	configAuth := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}
	compatAuth := &Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "Acme", "provider_key": "Acme"}}
	for _, tc := range []struct {
		name, route string
		auth        *Auth
		want        bool
	}{
		{"provider matches", "provider", fileAuth, true},
		{"provider excludes", "provider", &Auth{Provider: "claude", FileName: "allowed.json"}, false},
		{"file matches", "file", fileAuth, true},
		{"path basename matches", "file", pathAuth, true},
		{"different file", "file", &Auth{Provider: "codex", FileName: "other.json"}, false},
		{"config key excluded with files", "file", configAuth, false},
		{"config key allowed by provider", "provider", configAuth, true},
		{"canonical compatibility name", "compat", compatAuth, true},
		{"original compatibility name", "original", compatAuth, true},
		{"unknown fails closed", "unknown", fileAuth, false},
		{"no route unchanged", "", configAuth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := coreexecutor.Options{}
			if tc.route != "" {
				opts.Metadata = map[string]any{coreexecutor.RouteMetadataKey: tc.route}
			}
			if got := authSelectionEligibilityForRequest(context.Background(), opts, snapshot).allows(tc.auth); got != tc.want {
				t.Fatalf("allows = %v, want %v", got, tc.want)
			}
		})
	}
	if authSelectionEligibilityForRequest(nil, coreexecutor.Options{Metadata: map[string]any{coreexecutor.RouteMetadataKey: 17}}, snapshot).allows(fileAuth) {
		t.Fatal("invalid route metadata did not fail closed")
	}
}

func TestEndpointRouteSelectionNoFallbackAndReload(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
	const model = "endpoint-route-selection-test"
	reg := registry.GetGlobalRegistry()
	for _, a := range []*Auth{{ID: model + "-allowed", Provider: "codex", FileName: "allowed.json", Status: StatusActive}, {ID: model + "-outside", Provider: "codex", FileName: "outside.json", Status: StatusActive}} {
		if _, errRegister := manager.Register(ctx, a); errRegister != nil {
			t.Fatal(errRegister)
		}
		reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model, Object: "model"}})
		t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	}
	manager.SetConfig(&config.Config{Routes: []config.RouteConfig{{Name: "selected", Path: "/selected", Providers: []string{"codex"}, AuthFiles: []string{"allowed.json"}}}})
	opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.RouteMetadataKey: "selected"}}
	selected, _, errPick := manager.pickNext(ctx, "codex", model, opts, nil)
	if errPick != nil || selected == nil || selected.FileName != "allowed.json" {
		t.Fatalf("selected = %v, error = %v", selected, errPick)
	}
	if fallback, _, errFallback := manager.pickNext(ctx, "codex", model, opts, map[string]struct{}{selected.ID: {}}); errFallback == nil || fallback != nil {
		t.Fatalf("escaped route: %v, %v", fallback, errFallback)
	}
	manager.SetConfig(&config.Config{})
	if stale, _, errStale := manager.pickNext(ctx, "codex", model, opts, nil); errStale == nil || stale != nil {
		t.Fatalf("removed route allowed auth: %v, %v", stale, errStale)
	}
	if normal, _, errNormal := manager.pickNext(ctx, "codex", model, coreexecutor.Options{}, nil); errNormal != nil || normal == nil {
		t.Fatalf("unrouted selection changed: %v", errNormal)
	}
}
