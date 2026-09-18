// Package analyze runs every new announcement through an LLM, decides whether
// it is an exchange-API change that the trading terminal must react to, and
// files an urgent ClickUp task describing what changed and what to do.
//
// The project is stdlib-only by design (hermetic Docker build, no module
// download), so the model providers are called over plain HTTPS rather than
// through their SDKs. Three OpenAI-compatible-or-not providers are supported:
// anthropic (Messages API), openai and deepseek (chat completions).
package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"exchangebot/internal/httpx"
)

// Provider selects which vendor API to call.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderDeepSeek  Provider = "deepseek"
)

// DefaultModel is what each provider runs when LLM_MODEL is empty.
var DefaultModel = map[Provider]string{
	ProviderAnthropic: "claude-opus-5",
	ProviderOpenAI:    "gpt-5",
	ProviderDeepSeek:  "deepseek-v4-pro",
}

// LLM is a minimal "system + user → text" completion client.
type LLM struct {
	provider Provider
	apiKey   string
	model    string
	baseURL  string
	http     *httpx.Client
}

// NewLLM builds a client. baseURL overrides the vendor default (self-hosted
// gateways); model falls back to DefaultModel.
func NewLLM(provider Provider, apiKey, model, baseURL string, hc *httpx.Client) (*LLM, error) {
	switch provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek:
	case "":
		provider = ProviderAnthropic
	default:
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q (anthropic|openai|deepseek)", provider)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY is required for provider %s", provider)
	}
	if model == "" {
		model = DefaultModel[provider]
	}
	if baseURL == "" {
		switch provider {
		case ProviderAnthropic:
			baseURL = "https://api.anthropic.com"
		case ProviderOpenAI:
			baseURL = "https://api.openai.com"
		case ProviderDeepSeek:
			baseURL = "https://api.deepseek.com"
		}
	}
	return &LLM{provider: provider, apiKey: apiKey, model: model, baseURL: strings.TrimRight(baseURL, "/"), http: hc}, nil
}

// Model reports the resolved model name (for logs).
func (l *LLM) Model() string { return l.model }

// Provider reports the vendor (for logs).
func (l *LLM) Provider() Provider { return l.provider }

// Complete returns the model's text answer. Callers ask for JSON in the prompt
// and parse it themselves — that keeps one code path for all three vendors.
func (l *LLM) Complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	switch l.provider {
	case ProviderAnthropic:
		return l.anthropic(ctx, system, user, maxTokens)
	default:
		return l.openAICompatible(ctx, system, user, maxTokens)
	}
}

func (l *LLM) anthropic(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":      l.model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	body, _ := json.Marshal(payload)
	resp, err := l.http.Post(ctx, l.baseURL+"/v1/messages", body, map[string]string{
		"x-api-key":         l.apiKey,
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return "", fmt.Errorf("anthropic: %w", err)
	}
	var out struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", fmt.Errorf("anthropic decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic: %s", out.Error.Message)
	}
	if out.StopReason == "max_tokens" {
		return "", fmt.Errorf("anthropic: answer truncated at %d tokens", maxTokens)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("anthropic: empty answer")
	}
	return sb.String(), nil
}

func (l *LLM) openAICompatible(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model": l.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	if l.provider == ProviderDeepSeek {
		// DeepSeek: thinking mode at the highest effort; max_tokens there covers
		// reasoning + answer, so give it room.
		payload["max_tokens"] = max(maxTokens, 32000)
		payload["thinking"] = map[string]string{"type": "enabled"}
		payload["reasoning_effort"] = "max"
	} else {
		payload["max_completion_tokens"] = maxTokens
	}
	body, _ := json.Marshal(payload)
	resp, err := l.http.Post(ctx, l.baseURL+"/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer " + l.apiKey,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", l.provider, err)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", fmt.Errorf("%s decode: %w", l.provider, err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s: %s", l.provider, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: empty answer", l.provider)
	}
	if out.Choices[0].FinishReason == "length" {
		return "", fmt.Errorf("%s: answer truncated at %d tokens", l.provider, maxTokens)
	}
	return out.Choices[0].Message.Content, nil
}

// extractJSON pulls the first JSON object out of a model answer, tolerating
// ```json fences and prose around it.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
