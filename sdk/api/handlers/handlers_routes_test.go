package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func endpointRouteContext(config routes.Config) context.Context {
	var store routes.Store
	store.Update([]routes.Config{config})
	var ctx context.Context
	store.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, config.Path+"/models", nil))
	return ctx
}

func TestEndpointRouteRequestMetadata(t *testing.T) {
	ctx := endpointRouteContext(routes.Config{Name: "cyber", Path: "/cyber"})
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{{"direct", ctx}, {"gin", context.WithValue(context.Background(), "gin", &gin.Context{Request: httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctx)})}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestExecutionMetadata(tc.ctx)[coreexecutor.RouteMetadataKey]; got != "cyber" {
				t.Fatalf("route metadata = %v", got)
			}
		})
	}
	if _, exists := requestExecutionMetadata(context.Background())[coreexecutor.RouteMetadataKey]; exists {
		t.Fatal("unrouted metadata contains route")
	}
}

func TestEndpointRouteProvidersForExecution(t *testing.T) {
	const model = "endpoint-route-intersection-test"
	reg := registry.GetGlobalRegistry()
	for _, provider := range []string{"codex", "claude"} {
		id := model + provider
		reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	ctx := endpointRouteContext(routes.Config{Name: "cyber", Path: "/cyber", Providers: []string{"codex"}})
	providers, gotModel, errMsg := h.providersForExecution(ctx, model, model, false, modelRouteDecision{}, modelExecutionOptions{})
	if errMsg != nil || gotModel != model || !reflect.DeepEqual(providers, []string{"codex"}) {
		t.Fatalf("providers=%v model=%s error=%v", providers, gotModel, errMsg)
	}
	for _, opts := range []modelExecutionOptions{{}, {ForcedProvider: "claude"}} {
		badContext := endpointRouteContext(routes.Config{Name: "bad\"route", Path: "/bad", Providers: []string{"gemini"}})
		_, _, errUnavailable := h.providersForExecution(badContext, model, "model\"injection", false, modelRouteDecision{}, opts)
		if errUnavailable == nil || errUnavailable.StatusCode != http.StatusBadRequest {
			t.Fatalf("error = %v", errUnavailable)
		}
		if code := ojson.Get(errUnavailable.Error.Error(), "error.code").String(); code != "model_not_available_on_route" {
			t.Fatalf("code = %q, body = %s", code, errUnavailable.Error)
		}
	}
	all, _, errAll := h.providersForExecution(context.Background(), model, model, false, modelRouteDecision{}, modelExecutionOptions{})
	if errAll != nil || len(all) != 2 {
		t.Fatalf("unrouted providers = %v, error = %v", all, errAll)
	}
}

func TestEndpointRouteModelListFormatsAndUnroutedBytes(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	configs := []routes.Config{{Name: "selected", Path: "/selected", AuthFiles: []string{"selected.json"}}}
	manager.SetRoutes(configs)
	reg := registry.GetGlobalRegistry()
	for _, tc := range []struct{ id, file, model string }{{t.Name() + "-selected", "selected.json", "selected-model"}, {t.Name() + "-outside", "outside.json", "outside-model"}} {
		a := &coreauth.Auth{ID: tc.id, Provider: "codex", FileName: tc.file, Status: coreauth.StatusActive}
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatal(errRegister)
		}
		reg.RegisterClient(tc.id, "codex", []*registry.ModelInfo{{ID: tc.model}})
		t.Cleanup(func() { reg.UnregisterClient(tc.id) })
	}
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	for _, tc := range []struct{ name, format, body, path string }{
		{"openai", "openai", `{ "data": [{"id":"selected-model"},{"id":"outside-model"}], "object":"list" }`, "data"},
		{"codex", "openai", `{"models":[{"slug":"selected-model"},{"slug":"outside-model"}]}`, "models"},
		{"gemini", "gemini", `{"models":[{"name":"models/selected-model"},{"name":"models/outside-model"}]}`, "models"},
		{"claude", "claude", `{"data":[{"id":"claude-fable-5-dd-ledom-detceles"},{"id":"claude-fable-5-dd-ledom-edistuo"}],"first_id":"claude-fable-5-dd-ledom-detceles","last_id":"claude-fable-5-dd-ledom-edistuo"}`, "data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, routed := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				if routed {
					c.Request = c.Request.WithContext(endpointRouteContext(configs[0]))
				}
				out := h.filterRouteModelList(c, tc.format, []byte(tc.body))
				if !routed {
					if string(out) != tc.body {
						t.Fatalf("unrouted bytes changed: %s", out)
					}
					continue
				}
				if count := len(ojson.GetBytes(out, tc.path).Array()); count != 1 {
					t.Fatalf("filtered count = %d, body = %s", count, out)
				}
				if tc.format == "claude" && ojson.GetBytes(out, "last_id").String() != "claude-fable-5-dd-ledom-detceles" {
					t.Fatalf("Claude cursor not filtered: %s", out)
				}
			}
		})
	}
}
