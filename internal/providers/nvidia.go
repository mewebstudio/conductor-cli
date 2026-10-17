package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/secrets"
)

// NVIDIABaseURL is the OpenAI-compatible NVIDIA API catalog endpoint.
const NVIDIABaseURL = "https://integrate.api.nvidia.com/v1"

// NVIDIA calls models hosted in NVIDIA's API catalog. It cannot read the
// repository, so review prompts carry the diff inline.
type NVIDIA struct{}

func (*NVIDIA) Name() string           { return "nvidia" }
func (*NVIDIA) Description() string    { return "NVIDIA API catalog (open models), text only" }
func (*NVIDIA) Capabilities() []string { return []string{"review", "ask"} }
func (*NVIDIA) RepoAccess() bool       { return false }
func (*NVIDIA) NeedsKey() bool         { return true }

func (*NVIDIA) Check() error {
	if _, err := secrets.Get("nvidia"); err != nil {
		return fmt.Errorf("no API key stored (run `conductor providers key nvidia`): %w", err)
	}
	return nil
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// ErrBadKey means the API rejected the key.
var ErrBadKey = errors.New("API key rejected")

// NVIDIAModels lists the model ids the key can use; it doubles as key validation.
func NVIDIAModels(ctx context.Context, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, NVIDIABaseURL+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrBadKey
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing models: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// SuggestNVIDIAModel picks a sensible default for code review from the list.
func SuggestNVIDIAModel(ids []string) string {
	for _, want := range []string{"qwen3-coder", "deepseek-v3", "deepseek-r1", "kimi-k2", "glm-4", "llama-3.3-nemotron", "llama-3.1-405b"} {
		for _, id := range ids {
			if strings.Contains(strings.ToLower(id), want) {
				return id
			}
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

const systemPrompt = "You are an independent senior engineer. You only read and advise; never claim to have changed files."

func (n *NVIDIA) Run(ctx context.Context, r Request) (Status, error) {
	key, err := secrets.Get("nvidia")
	if err != nil {
		return NotConfigured, fmt.Errorf("no NVIDIA API key stored: %w", err)
	}
	if r.Model == "" {
		return NotConfigured, errors.New("no NVIDIA model chosen (run `conductor review set nvidia <model>`)")
	}
	prompt, err := os.ReadFile(r.PromptFile)
	if err != nil {
		return Failed, err
	}
	payload, _ := json.Marshal(map[string]any{
		"model": r.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(prompt)},
		},
		"temperature": 0.2,
		"max_tokens":  4096,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, NVIDIABaseURL+"/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Failed, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := ensureParent(r.OutputFile); err != nil {
		return Failed, err
	}
	_ = os.WriteFile(logPath(r.OutputFile), raw, 0o644)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return Limited, errors.New("NVIDIA rate limit reached")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return NotConfigured, ErrBadKey
	case resp.StatusCode != http.StatusOK:
		return Failed, fmt.Errorf("NVIDIA HTTP %d (see %s)", resp.StatusCode, logPath(r.OutputFile))
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Choices) == 0 {
		return Failed, fmt.Errorf("unexpected NVIDIA response (see %s)", logPath(r.OutputFile))
	}
	return OK, os.WriteFile(r.OutputFile, []byte(body.Choices[0].Message.Content+"\n"), 0o644)
}
