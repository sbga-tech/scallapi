package handlers

import (
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
)

// filterRouteModelList runs at the shared model-list response boundary, after
// protocol formatting and interceptors, covering client-specific catalogs too.
// Unrouted responses return their original bytes without decoding or rewriting.
func (h *BaseAPIHandler) filterRouteModelList(c *gin.Context, sourceFormat string, body []byte) []byte {
	if c == nil || c.Request == nil {
		return body
	}
	route := routes.FromContext(c.Request.Context())
	if route == nil {
		return body
	}
	ids := h.AuthManager.RouteModelIDs(route.Name())
	doc := ojson.NewDoc(body)
	first, last := "", ""
	for _, field := range []string{"data", "models"} {
		entries := doc.Get(field)
		if !entries.IsArray() {
			continue
		}
		values := entries.Array()
		kept := make([]json.RawMessage, 0, len(values))
		for _, value := range values {
			id := value.Get("id").String()
			if id == "" {
				id = value.Get("slug").String()
			}
			if id == "" {
				id = strings.TrimPrefix(value.Get("name").String(), "models/")
			}
			registeredID := id
			if sourceFormat == "claude" {
				registeredID = claudemodels.ResolveClaudeModelIDPrefix(id)
			}
			if _, ok := ids[registeredID]; !ok {
				continue
			}
			kept = append(kept, json.RawMessage(value.Raw))
			if first == "" {
				first = id
			}
			last = id
		}
		_ = doc.Set(field, kept)
	}
	// Anthropic pagination cursors must reference the filtered catalog.
	if doc.Get("first_id").Exists() {
		_ = doc.Set("first_id", first)
	}
	if doc.Get("last_id").Exists() {
		_ = doc.Set("last_id", last)
	}
	return doc.Bytes()
}
