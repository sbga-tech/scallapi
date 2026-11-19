package registry

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"gopkg.in/yaml.v3"
)

//go:embed models/modelsdev_overlay.yaml
var modelsDevOverlayYAML []byte

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	ReleaseDate      string `json:"release_date"`
	Status           string `json:"status"`
	Reasoning        bool   `json:"reasoning"`
	ToolCall         bool   `json:"tool_call"`
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
		Min    int      `json:"min"`
		Max    int      `json:"max"`
	} `json:"reasoning_options"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
}

type modelsDevChannel struct {
	Source        string              `yaml:"source"`
	Providers     []string            `yaml:"providers"`
	Include       string              `yaml:"include"`
	Exclude       string              `yaml:"exclude"`
	Allowlist     []string            `yaml:"allowlist"`
	Type          string              `yaml:"type"`
	OwnedBy       string              `yaml:"owned-by"`
	Caps          map[string]int      `yaml:"caps"`
	Overrides     []modelsDevOverride `yaml:"overrides"`
	ExtraEmbedded []string            `yaml:"extra-embedded"`
	ExtraModels   []map[string]any    `yaml:"extra-models"`
}

type modelsDevOverride struct {
	Pattern string         `yaml:"pattern"`
	Fields  map[string]any `yaml:"fields"`
}

// decodeGeneralCatalog detects formats by content, never by the configured URL.
// CPA payloads retain their exact contents and bypass models.dev's loss guard.
func decodeGeneralCatalog(data []byte) (*staticModelsJSON, bool, error) {
	var root map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(data, &root); errUnmarshal != nil {
		return nil, false, errUnmarshal
	}
	if len(root) == 0 {
		return nil, false, fmt.Errorf("empty model catalog")
	}
	cpa, dev := false, false
	for _, value := range root {
		value = bytes.TrimSpace(value)
		if len(value) > 0 && value[0] == '[' {
			cpa = true
		} else if len(value) > 0 && value[0] == '{' {
			var provider map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(value, &provider); errUnmarshal != nil {
				return nil, false, errUnmarshal
			}
			if models, ok := provider["models"]; ok && bytes.HasPrefix(bytes.TrimSpace(models), []byte("{")) {
				dev = true
			}
		}
	}
	if cpa && dev || !cpa && !dev {
		return nil, false, fmt.Errorf("unrecognized or mixed model catalog format")
	}
	if dev {
		converted, errConvert := convertModelsDev(data, modelsDevOverlayYAML)
		if errConvert != nil {
			return nil, true, errConvert
		}
		data = converted
	}
	var parsed staticModelsJSON
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return nil, dev, errUnmarshal
	}
	return &parsed, dev, validateModelsCatalog(&parsed)
}

func convertModelsDev(data, overlay []byte) ([]byte, error) {
	var feed map[string]modelsDevProvider
	if errUnmarshal := json.Unmarshal(data, &feed); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	var channels map[string]modelsDevChannel
	if errUnmarshal := yaml.Unmarshal(overlay, &channels); errUnmarshal != nil {
		return nil, fmt.Errorf("models.dev overlay: %w", errUnmarshal)
	}
	var embedded map[string][]json.RawMessage
	if errUnmarshal := json.Unmarshal(embeddedModelsJSON, &embedded); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	out := ojson.NewDoc([]byte("{}"))
	for channel, rule := range channels {
		models, errConvert := convertModelsDevChannel(feed, embedded[channel], rule)
		if errConvert != nil {
			return nil, fmt.Errorf("models.dev channel %s: %w", channel, errConvert)
		}
		if errSet := out.Set(channel, models); errSet != nil {
			return nil, errSet
		}
	}
	return out.Bytes(), nil
}

func convertModelsDevChannel(feed map[string]modelsDevProvider, embedded []json.RawMessage, rule modelsDevChannel) ([]json.RawMessage, error) {
	if rule.Source == "embedded" {
		return embedded, nil
	}
	compile := func(pattern string) (*regexp.Regexp, error) {
		if pattern == "" {
			return nil, nil
		}
		return regexp.Compile(pattern)
	}
	include, errInclude := compile(rule.Include)
	if errInclude != nil {
		return nil, errInclude
	}
	exclude, errExclude := compile(rule.Exclude)
	if errExclude != nil {
		return nil, errExclude
	}
	patterns := make([]*regexp.Regexp, len(rule.Overrides))
	for i, override := range rule.Overrides {
		pattern, errCompile := regexp.Compile(override.Pattern)
		if errCompile != nil {
			return nil, errCompile
		}
		patterns[i] = pattern
	}
	allow := make(map[string]bool, len(rule.Allowlist))
	for _, id := range rule.Allowlist {
		allow[id] = true
	}
	fallback := make(map[string]json.RawMessage, len(embedded))
	for _, raw := range embedded {
		var model ModelInfo
		if errUnmarshal := json.Unmarshal(raw, &model); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		fallback[model.ID] = raw
	}
	models := make(map[string]json.RawMessage)
	apply := func(id string, raw []byte) error {
		if len(allow) > 0 && !allow[id] || include != nil && !include.MatchString(id) || exclude != nil && exclude.MatchString(id) {
			return nil
		}
		doc := ojson.NewDoc(raw)
		// These endpoint capabilities are not described by models.dev. Keep the
		// embedded evidence, rather than inferring web-search or backend features.
		if previous := fallback[id]; previous != nil {
			var fields map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(previous, &fields); errUnmarshal != nil {
				return errUnmarshal
			}
			for _, field := range []string{"native_capabilities", "support_configuration_update", "config"} {
				if value, ok := fields[field]; ok {
					if errSet := doc.SetRaw(field, string(value)); errSet != nil {
						return errSet
					}
				}
			}
		}
		for i, override := range rule.Overrides {
			if patterns[i].MatchString(id) {
				for path, value := range override.Fields {
					if errSet := doc.Set(path, value); errSet != nil {
						return errSet
					}
				}
			}
		}
		for field, cap := range rule.Caps {
			if cap <= 0 {
				return fmt.Errorf("invalid cap for %s", field)
			}
			if doc.Get(field).Int() > int64(cap) {
				if errSet := doc.Set(field, cap); errSet != nil {
					return errSet
				}
			}
		}
		models[id] = doc.Bytes()
		return nil
	}
	for _, provider := range rule.Providers {
		for key, model := range feed[provider].Models {
			if model.Status == "deprecated" {
				continue
			}
			if model.ID == "" {
				model.ID = key
			}
			mapped := mapModelsDevModel(model, rule)
			raw, errMarshal := json.Marshal(mapped)
			if errMarshal != nil {
				return nil, errMarshal
			}
			if errApply := apply(model.ID, raw); errApply != nil {
				return nil, errApply
			}
		}
	}
	for _, id := range rule.ExtraEmbedded {
		if _, exists := models[id]; exists {
			continue
		}
		// Explicit extras are only for absent IDs, never resurrect deprecated IDs.
		present := false
		for _, provider := range rule.Providers {
			if _, exists := feed[provider].Models[id]; exists {
				present = true
			}
		}
		if present {
			continue
		}
		raw, ok := fallback[id]
		if !ok {
			return nil, fmt.Errorf("embedded extra %q not found", id)
		}
		if errApply := apply(id, raw); errApply != nil {
			return nil, errApply
		}
	}
	for _, extra := range rule.ExtraModels {
		id, _ := extra["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("extra model has no id")
		}
		raw, errMarshal := json.Marshal(extra)
		if errMarshal != nil {
			return nil, errMarshal
		}
		if errApply := apply(id, raw); errApply != nil {
			return nil, errApply
		}
	}
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		result = append(result, models[id])
	}
	return result, nil
}

func mapModelsDevModel(model modelsDevModel, rule modelsDevChannel) *ModelInfo {
	mapped := &ModelInfo{ID: model.ID, Object: "model", Type: rule.Type, OwnedBy: rule.OwnedBy,
		DisplayName: model.Name, Description: model.Description, ContextLength: model.Limit.Context,
		MaxCompletionTokens: model.Limit.Output, SupportedInputModalities: model.Modalities.Input,
		SupportedOutputModalities: model.Modalities.Output}
	if released, errParse := time.Parse("2006-01-02", model.ReleaseDate); errParse == nil {
		mapped.Created = released.Unix()
	}
	if model.ToolCall && rule.Type == "openai" {
		mapped.SupportedParameters = []string{"tools"}
	}
	if rule.Type == "gemini" {
		mapped.Name = "models/" + model.ID
		mapped.InputTokenLimit = model.Limit.Input
		if mapped.InputTokenLimit == 0 {
			mapped.InputTokenLimit = model.Limit.Context
		}
		mapped.OutputTokenLimit = model.Limit.Output
		mapped.SupportedGenerationMethods = []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"}
	}
	if model.Reasoning {
		mapped.Thinking = &ThinkingSupport{}
		for _, option := range model.ReasoningOptions {
			switch option.Type {
			case "effort":
				mapped.Thinking.Levels = option.Values
			case "toggle":
				mapped.Thinking.ZeroAllowed = true
			case "budget_tokens":
				mapped.Thinking.Min, mapped.Thinking.Max = option.Min, option.Max
			}
		}
	}
	return mapped
}

func guardModelsDevCatalog(previous, next *staticModelsJSON) error {
	if previous == nil {
		return nil
	}
	sections := [...]struct {
		name          string
		before, after int
	}{
		{"claude", len(previous.Claude), len(next.Claude)},
		{"gemini", len(previous.Gemini), len(next.Gemini)},
		{"vertex", len(previous.Vertex), len(next.Vertex)},
		{"aistudio", len(previous.AIStudio), len(next.AIStudio)},
		{"codex-free", len(previous.CodexFree), len(next.CodexFree)},
		{"codex-team", len(previous.CodexTeam), len(next.CodexTeam)},
		{"codex-plus", len(previous.CodexPlus), len(next.CodexPlus)},
		{"codex-pro", len(previous.CodexPro), len(next.CodexPro)},
		{"kimi", len(previous.Kimi), len(next.Kimi)},
		{"antigravity", len(previous.Antigravity), len(next.Antigravity)},
		{"xai", len(previous.XAI), len(next.XAI)},
		{"meta", len(previous.Meta), len(next.Meta)},
		{"devin", len(previous.Devin), len(next.Devin)},
	}
	for _, section := range sections {
		if section.before > 0 && (section.after == 0 || section.after*2 < section.before) {
			return fmt.Errorf("models.dev %s shrank from %d to %d models", section.name, section.before, section.after)
		}
	}
	return nil
}
