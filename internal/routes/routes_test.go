package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEndpointRoutesLongestSegmentPrefix(t *testing.T) {
	snapshot := Compile([]Config{{Name: "cyber", Path: "/cyber", RewriteTo: "/v1"}, {Name: "nested", Path: "/cyber/deep"}})
	for _, tc := range []struct{ path, want string }{{"/cyber", "cyber"}, {"/cyber/responses", "cyber"}, {"/cyber/deep/models", "nested"}, {"/cyberx", ""}, {"/other", ""}} {
		if got := snapshot.Match(tc.path).Name(); got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestEndpointRouteWrapperRewritesGinAndContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ prefix, rewrite, input, path, raw string }{
		{"/cyber", "/v1", "/cyber/models", "/v1/models", ""},
		{"/cyber", "", "/cyber/v1/models", "/v1/models", ""},
		{"/cyber", "", "/cyber", "/", ""},
		{"/cyber", "/v1", "/cy%62er/models/a%2Fb", "/v1/models/a/b", "/v1/models/a%2Fb"},
	} {
		t.Run(tc.input+tc.rewrite, func(t *testing.T) {
			var store Store
			store.Update([]Config{{Name: "selected", Path: tc.prefix, RewriteTo: tc.rewrite}})
			engine := gin.New()
			engine.GET(tc.path, func(c *gin.Context) {
				if got := FromContext(c.Request.Context()).Name(); got != "selected" {
					t.Errorf("route = %q", got)
				}
				if c.Request.URL.RawPath != tc.raw {
					t.Errorf("RawPath = %q, want %q", c.Request.URL.RawPath, tc.raw)
				}
				c.Status(http.StatusNoContent)
			})
			rec := httptest.NewRecorder()
			store.Wrap(engine).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.input, nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestEndpointRouteWrapperUnmatchedAndReload(t *testing.T) {
	var store Store
	configs := []Config{{Name: "old", Path: "/old", Providers: []string{"codex"}, Credentials: []string{"old.json"}}}
	store.Update(configs)
	before := store.Load()
	configs[0].Providers[0] = "claude"
	if !before.Named("old").AllowsProvider("codex") {
		t.Fatal("snapshot retained mutable config")
	}
	req := httptest.NewRequest(http.MethodGet, "/untouched?q=1", nil)
	originalURL := req.URL
	store.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r != req || r.URL != originalURL || FromContext(r.Context()) != nil {
			t.Fatal("unmatched request changed")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	store.Update([]Config{{Name: "new", Path: "/new"}})
	if store.Load().Named("old") != nil || store.Load().Match("/new").Name() != "new" || before.Named("old") == nil {
		t.Fatal("snapshot swap failed")
	}
}
