package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestEndpointRoutesHTTPWrapperAndReload(t *testing.T) {
	cfg := &config.Config{Routes: []config.RouteConfig{{Name: "cyber", Path: "/cyber", RewriteTo: "/target"}}}
	server := newTestServerWithConfig(t, cfg, WithRouterConfigurator(func(engine *gin.Engine, _ *handlers.BaseAPIHandler, _ *config.Config) {
		engine.GET("/target/echo", func(c *gin.Context) { c.String(http.StatusOK, routes.FromContext(c.Request.Context()).Name()) })
	}))
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{{"/cyber/echo", http.StatusOK, "cyber"}, {"/cyberx/echo", http.StatusNotFound, ""}, {"/target/echo", http.StatusOK, ""}} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status || (tc.status == http.StatusOK && rec.Body.String() != tc.body) {
			t.Fatalf("%s: status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	next := server.getConfig().CloneForRuntime()
	next.Routes = []config.RouteConfig{{Name: "reloaded", Path: "/new", RewriteTo: "/target"}}
	server.UpdateClients(next)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/cyber/echo", http.StatusNotFound}, {"/new/echo", http.StatusOK}} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("after reload %s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestEndpointRoutesWebsocketUpgrade(t *testing.T) {
	cfg := &config.Config{Routes: []config.RouteConfig{{Name: "socket", Path: "/socket", RewriteTo: "/target"}}}
	server := newTestServerWithConfig(t, cfg, WithRouterConfigurator(func(engine *gin.Engine, _ *handlers.BaseAPIHandler, _ *config.Config) {
		engine.GET("/target/ws", func(c *gin.Context) {
			upgrader := websocket.Upgrader{}
			conn, errUpgrade := upgrader.Upgrade(c.Writer, c.Request, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			defer func() { _ = conn.Close() }()
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(routes.FromContext(c.Request.Context()).Name())); errWrite != nil {
				t.Errorf("write: %v", errWrite)
			}
		})
	}))
	upstream := httptest.NewServer(server.Handler())
	defer upstream.Close()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http")+"/socket/ws", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = conn.Close() }()
	_, body, errRead := conn.ReadMessage()
	if errRead != nil || string(body) != "socket" {
		t.Fatalf("body = %q, error = %v", body, errRead)
	}
}

func TestEndpointRoutesFilterAllModelCatalogs(t *testing.T) {
	cfg := &config.Config{Routes: []config.RouteConfig{{Name: "selected", Path: "/selected", AuthFiles: []string{"selected.json"}}}}
	cfg.APIKeys = []string{"test-key"}
	server := newTestServerWithConfig(t, cfg)
	reg := registry.GetGlobalRegistry()
	for _, entry := range []struct{ id, file, model string }{{t.Name() + "-selected", "selected.json", "route-selected-model"}, {t.Name() + "-outside", "outside.json", "route-outside-model"}} {
		_, errRegister := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{ID: entry.id, Provider: "codex", FileName: entry.file, Status: auth.StatusActive})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		reg.RegisterClient(entry.id, "codex", []*registry.ModelInfo{{ID: entry.model, Object: "model", OwnedBy: "openai", Type: "codex"}})
		t.Cleanup(func() { reg.UnregisterClient(entry.id) })
	}
	for _, tc := range []struct{ name, path, header, value, array string }{
		{"openai", "/v1/models", "", "", "data"},
		{"claude", "/v1/models", "Anthropic-Version", "2023-06-01", "data"},
		{"codex", "/v1/models?client_version=cpa", "", "", "models"},
		{"gemini", "/v1beta/models", "", "", "models"},
		{"muse", "/muse-code/models", "", "", "data"},
		{"grok", "/v1/models", "User-Agent", "grok-shell/0.2.119", "data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, prefix := range []string{"", "/selected"} {
				req := httptest.NewRequest(http.MethodGet, prefix+tc.path, nil)
				req.Header.Set("Authorization", "Bearer test-key")
				if tc.header != "" {
					req.Header.Set(tc.header, tc.value)
				}
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", prefix+tc.path, rec.Code, rec.Body.String())
				}
				models := ojson.GetBytes(rec.Body.Bytes(), tc.array).Array()
				if prefix == "" && len(models) < 2 {
					t.Fatalf("unrouted catalog lost models: %s", rec.Body.String())
				}
				if prefix != "" && len(models) != 1 {
					t.Fatalf("routed catalog not filtered: %s", rec.Body.String())
				}
			}
		})
	}
}
