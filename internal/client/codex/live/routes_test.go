package live

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEndpointRouteLiveSelectionAndRetainedEligibility(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	cfg := routes.Config{Name: "selected", Path: "/selected", AuthFiles: []string{"selected.json"}}
	manager.SetRoutes([]routes.Config{cfg})
	selectedAuth := &auth.Auth{ID: t.Name() + "-selected", Provider: "codex", FileName: "selected.json", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}
	outsideAuth := &auth.Auth{ID: t.Name() + "-outside", Provider: "codex", FileName: "outside.json", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}
	for _, a := range []*auth.Auth{selectedAuth, outsideAuth} {
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	var store routes.Store
	store.Update([]routes.Config{cfg})
	var ctx context.Context
	store.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/selected/ws", nil))
	handler := NewHandler(manager, nil)
	_, selected, errSelect := handler.selectOAuth(ctx, "", coreexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != selectedAuth.ID {
		t.Fatalf("selected = %v, error = %v", selected, errSelect)
	}
	if !handler.routeAllowsAuth(ctx, selectedAuth) || handler.routeAllowsAuth(ctx, outsideAuth) {
		t.Fatal("retained auth escaped endpoint scope")
	}
	if !handler.routeAllowsAuth(context.Background(), outsideAuth) {
		t.Fatal("unrouted retained auth changed")
	}
	manager.SetRoutes(nil)
	if handler.routeAllowsAuth(ctx, selectedAuth) {
		t.Fatal("unknown route allowed retained auth")
	}
}
