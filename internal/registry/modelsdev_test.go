package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func TestModelsDevFieldMappingAndDeprecated(t *testing.T) {
	data := []byte(`{"example":{"models":{"reasoner":{"id":"reasoner","name":"Reasoner","description":"A model","reasoning":true,"tool_call":true,"reasoning_options":[{"type":"effort","values":["low","high"]},{"type":"toggle"},{"type":"budget_tokens","min":128,"max":8192}],"modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":100000,"input":90000,"output":10000},"release_date":"2026-10-01"},"old":{"id":"old","status":"deprecated"}}}}`)
	converted, errConvert := convertModelsDev(data, []byte("claude:\n  providers: [example]\n  type: openai\n  owned-by: example\n"))
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	var parsed staticModelsJSON
	if errUnmarshal := json.Unmarshal(converted, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(parsed.Claude) != 1 {
		t.Fatalf("deprecated model retained: %s", converted)
	}
	model := parsed.Claude[0]
	created, _ := time.Parse("2006-01-02", "2026-10-01")
	if model.ID != "reasoner" || model.DisplayName != "Reasoner" || model.Description != "A model" || model.Object != "model" || model.Type != "openai" || model.OwnedBy != "example" || model.Created != created.Unix() || model.ContextLength != 100000 || model.MaxCompletionTokens != 10000 {
		t.Fatalf("incorrect mapped fields: %+v", model)
	}
	if !reflect.DeepEqual(model.SupportedInputModalities, []string{"text", "image"}) || !reflect.DeepEqual(model.SupportedOutputModalities, []string{"text"}) || !reflect.DeepEqual(model.SupportedParameters, []string{"tools"}) {
		t.Fatalf("incorrect modalities/tools: %+v", model)
	}
	if !reflect.DeepEqual(model.Thinking, &ThinkingSupport{Min: 128, Max: 8192, ZeroAllowed: true, Levels: []string{"low", "high"}}) {
		t.Fatalf("incorrect thinking: %+v", model.Thinking)
	}
}

func TestModelsDevOverlayRules(t *testing.T) {
	data := []byte(`{"example":{"models":{"keep-one":{"id":"keep-one","name":"One","limit":{"context":1000000}},"keep-two":{"id":"keep-two"},"keep-blocked":{"id":"keep-blocked"},"other":{"id":"other"}}}}`)
	overlay := []byte(`claude:
  providers: [example]
  include: '^keep-'
  exclude: 'blocked$'
  allowlist: [keep-one, keep-blocked, other, extra]
  caps: {context_length: 272000}
  overrides:
    - pattern: '^keep-'
      fields: {display_name: Overridden, thinking.min: 1024}
  extra-models:
    - {id: extra, object: model, display_name: Extra}
gemini-cli:
  source: embedded
antigravity:
  source: embedded
`)
	// Let the extra through the include pattern, while testing rejection of
	// unrelated provider IDs independently of the explicit allowlist.
	overlay = []byte(strings.Replace(string(overlay), "'^keep-'", "'^(keep-|extra$)'", 1))
	converted, errConvert := convertModelsDev(data, overlay)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	var parsed map[string][]*ModelInfo
	if errUnmarshal := json.Unmarshal(converted, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(parsed["claude"]) != 2 {
		t.Fatalf("filters/extra failed: %s", converted)
	}
	if model := parsed["claude"][1]; model.ID != "keep-one" || model.ContextLength != 272000 || model.DisplayName != "Overridden" || model.Thinking.Min != 1024 {
		t.Fatalf("cap/override failed: %+v", model)
	}
	var embedded map[string][]*ModelInfo
	if errUnmarshal := json.Unmarshal(embeddedModelsJSON, &embedded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for _, channel := range []string{"antigravity", "gemini-cli"} {
		if !reflect.DeepEqual(parsed[channel], embedded[channel]) {
			t.Errorf("embedded channel %s changed", channel)
		}
	}
}

func TestModelsDevFormatDetectionAndPublication(t *testing.T) {
	previous := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previous
		modelsCatalogStore.mu.Unlock()
	})
	cpa := []byte(`{"claude":[{"id":"custom","display_name":"Custom","context_length":999999}]}`)
	parsed, converted, errDecode := decodeGeneralCatalog(cpa)
	if errDecode != nil || converted || parsed.Claude[0].ContextLength != 999999 {
		t.Fatalf("CPA detection: converted=%v err=%v", converted, errDecode)
	}
	if _, errPublish := publishCatalogBytes(cpa); errPublish != nil {
		t.Fatal(errPublish)
	}
	if model := getModels().Claude[0]; model.ID != "custom" || model.ContextLength != 999999 {
		t.Fatalf("CPA payload modified: %+v", model)
	}
	fixture, errRead := os.ReadFile("testdata/modelsdev_snapshot.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	parsed, converted, errDecode = decodeGeneralCatalog(fixture)
	if errDecode != nil || !converted {
		t.Fatalf("models.dev detection: converted=%v err=%v", converted, errDecode)
	}
	if _, errPublish := publishCatalogBytes(fixture); errPublish != nil {
		t.Fatal(errPublish)
	}
	if !reflect.DeepEqual(getModels(), parsed) {
		t.Fatal("converted payload was not published")
	}
	for _, invalid := range []string{`{}`, `{"random":{}}`, `{"claude":[],"provider":{"models":{}}}`} {
		if _, _, errDecode := decodeGeneralCatalog([]byte(invalid)); errDecode == nil {
			t.Errorf("accepted invalid format %s", invalid)
		}
	}
}

func TestModelsDevConditionalGET(t *testing.T) {
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"catalog-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"catalog-1"`)
		_, _ = w.Write(embeddedModelsJSON)
	}))
	defer server.Close()
	published := 0
	var retained []byte
	u := &catalogUpdater{fetchConditional: fetchGeneralCatalog, publish: func(data []byte) ([]string, error) {
		published++
		retained = data
		return nil, nil
	}}
	ctx := context.Background()
	u.refresh(ctx, server.URL, 0)
	first := retained
	status, errRefresh := u.refreshOnDemand(ctx, server.URL, nil)
	if errRefresh != nil || !status.Refreshed || len(status.ChangedProviders) != 0 || published != 1 || &first[0] != &retained[0] {
		t.Fatalf("304 published or failed: published=%d status=%+v err=%v", published, status, errRefresh)
	}
	if !reflect.DeepEqual(headers, []string{"", `"catalog-1"`}) {
		t.Fatalf("conditional headers: %v", headers)
	}
	// Publishing embedded data invalidates the active-source validator, so a
	// source switch back cannot leave the embedded catalog behind on a 304.
	u.refresh(ctx, embeddedCatalogSource, 0)
	u.refresh(ctx, server.URL, 0)
	if headers[2] != "" || published != 3 {
		t.Fatalf("source switch reused stale validator: headers=%v published=%d", headers, published)
	}
}

func TestModelsDevGuardRejectsTruncatedFeed(t *testing.T) {
	previous := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previous
		modelsCatalogStore.mu.Unlock()
	})
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &staticModelsJSON{Claude: make([]*ModelInfo, 20)}
	modelsCatalogStore.mu.Unlock()
	kept := getModels()
	if _, errPublish := publishCatalogBytes([]byte(`{"anthropic":{"models":{}}}`)); errPublish == nil {
		t.Fatal("accepted truncated feed")
	}
	if getModels() != kept {
		t.Fatal("rejected feed replaced the previous catalog")
	}
	for _, count := range []int{0, 9} {
		if errGuard := guardModelsDevCatalog(kept, &staticModelsJSON{Claude: make([]*ModelInfo, count)}); errGuard == nil {
			t.Errorf("accepted shrink to %d", count)
		}
	}
	if errGuard := guardModelsDevCatalog(kept, &staticModelsJSON{Claude: make([]*ModelInfo, 10)}); errGuard != nil {
		t.Fatalf("exactly half should be allowed: %v", errGuard)
	}
}

func TestModelsDevSnapshotAllChannels(t *testing.T) {
	fixture, errRead := os.ReadFile("testdata/modelsdev_snapshot.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	converted, errConvert := convertModelsDev(fixture, modelsDevOverlayYAML)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	var sections map[string][]*ModelInfo
	if errUnmarshal := json.Unmarshal(converted, &sections); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var embedded map[string][]*ModelInfo
	if errUnmarshal := json.Unmarshal(embeddedModelsJSON, &embedded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for channel, models := range embedded {
		if len(models) > 0 && len(sections[channel]) == 0 {
			t.Errorf("channel %s is empty", channel)
		}
	}
	parsed, _, errDecode := decodeGeneralCatalog(fixture)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if errGuard := guardModelsDevCatalog(getModels(), parsed); errGuard != nil {
		t.Fatalf("real snapshot rejected: %v", errGuard)
	}
}

func TestModelsDevGeneralSizeLimit(t *testing.T) {
	path := t.TempDir() + "/catalog.json"
	// Whitespace makes this a valid CPA catalog larger than the Codex limit.
	data := append([]byte(`{"claude":[{"id":"large"}]}`), []byte(strings.Repeat(" ", maxCodexClientModelsSize))...)
	if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, _, _, errFetch := fetchGeneralCatalog(context.Background(), path, nil); errFetch != nil {
		t.Fatalf("general catalog retained 8 MiB limit: %v", errFetch)
	}
	if _, errRead := readCatalogSource(context.Background(), path); errRead == nil {
		t.Fatal("Codex catalog lost its 8 MiB limit")
	}
}

func TestModelsDevRejectedResponseDoesNotRememberETag(t *testing.T) {
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"rejected"`)
		_, _ = w.Write([]byte(`{"anthropic":{"models":{}}}`))
	}))
	defer server.Close()
	u := &catalogUpdater{fetchConditional: fetchGeneralCatalog, publish: func([]byte) ([]string, error) {
		return nil, fmt.Errorf("catalog rejected")
	}}
	u.refresh(context.Background(), server.URL, 0)
	u.refresh(context.Background(), server.URL, 0)
	if !reflect.DeepEqual(headers, []string{"", ""}) {
		t.Fatalf("rejected response cached an ETag: %v", headers)
	}
}
