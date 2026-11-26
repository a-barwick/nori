package homeconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"nori/internal/homeaiwire"
)

const maxModels = 100
const maxRequestBytes = 8 << 20
const maxOutputBytes = 8 << 20

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,99}$`)

type Ollama struct {
	baseURL string
	client  *http.Client
}

func NewOllama(baseURL string) *Ollama {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 3 * time.Minute,
	}
	return &Ollama{
		baseURL: baseURL,
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type ollamaTags struct {
	Models []struct {
		Name         string   `json:"name"`
		Digest       string   `json:"digest"`
		RemoteModel  string   `json:"remote_model"`
		RemoteHost   string   `json:"remote_host"`
		Capabilities []string `json:"capabilities"`
	} `json:"models"`
}

type ollamaDetails struct {
	RemoteModel  string   `json:"remote_model"`
	RemoteHost   string   `json:"remote_host"`
	Capabilities []string `json:"capabilities"`
}

func (o *Ollama) Inventory(ctx context.Context) ([]homeaiwire.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to Ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama inventory returned HTTP %d", resp.StatusCode)
	}
	var tags ollamaTags
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&tags); err != nil {
		return nil, fmt.Errorf("read Ollama inventory: %w", err)
	}
	models := make([]homeaiwire.Model, 0, min(len(tags.Models), maxModels))
	seen := make(map[string]bool)
	for _, tag := range tags.Models {
		if len(models) == maxModels {
			break
		}
		if !modelName.MatchString(tag.Name) || tag.Digest == "" || len(tag.Digest) > 100 || strings.IndexFunc(tag.Digest, unicode.IsControl) >= 0 || seen[tag.Name] ||
			tag.RemoteModel != "" || tag.RemoteHost != "" || strings.HasSuffix(strings.ToLower(tag.Name), ":cloud") {
			continue
		}
		detail, err := o.show(ctx, tag.Name)
		if err != nil {
			continue
		}
		if detail.RemoteModel != "" || detail.RemoteHost != "" {
			continue
		}
		capabilities := detail.Capabilities
		if len(capabilities) == 0 {
			capabilities = tag.Capabilities
		}
		filteredCapabilities := make([]string, 0, min(len(capabilities), 20))
		for _, capability := range capabilities {
			if len(filteredCapabilities) == 20 {
				break
			}
			if len(capability) > 0 && len(capability) <= 64 && strings.IndexFunc(capability, unicode.IsControl) < 0 {
				filteredCapabilities = append(filteredCapabilities, capability)
			}
		}
		capabilities = filteredCapabilities
		seen[tag.Name] = true
		models = append(models, homeaiwire.Model{Name: tag.Name, Digest: tag.Digest, Capabilities: capabilities})
	}
	return models, nil
}

func (o *Ollama) show(ctx context.Context, model string) (ollamaDetails, error) {
	var details ollamaDetails
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return details, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return details, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return details, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return details, errors.New("model details unavailable")
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&details)
	return details, err
}

func (o *Ollama) Open(ctx context.Context, operation, model, digest string, payload []byte) (*http.Response, string) {
	if len(payload) == 0 || len(payload) > maxRequestBytes || !modelName.MatchString(model) || digest == "" {
		return nil, "invalid_request"
	}
	models, err := o.Inventory(ctx)
	if err != nil {
		return nil, "ollama_unavailable"
	}
	found := false
	for _, item := range models {
		if item.Name == model {
			if item.Digest != digest {
				return nil, "model_changed"
			}
			found = true
			break
		}
	}
	if !found {
		return nil, "model_unavailable"
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, "invalid_request"
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, "invalid_request"
	}
	if body["model"] != model || !validProviderInput(body, operation) {
		return nil, "invalid_request"
	}
	if store, ok := body["store"]; ok {
		if store != false {
			return nil, "invalid_request"
		}
		delete(body, "store")
	}
	path := ""
	switch operation {
	case "chat":
		if value, ok := body["max_completion_tokens"]; ok {
			if _, exists := body["max_tokens"]; exists {
				return nil, "invalid_request"
			}
			body["max_tokens"] = value
			delete(body, "max_completion_tokens")
		}
		path = "/v1/chat/completions"
	case "responses":
		path = "/v1/responses"
	default:
		return nil, "invalid_request"
	}
	payload, err = json.Marshal(body)
	if err != nil {
		return nil, "invalid_request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, "invalid_request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := o.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "deadline_exceeded"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, "cancelled"
		}
		return nil, "transport_error"
	}
	if operation != "chat" || resp.StatusCode != http.StatusNotImplemented {
		return resp, ""
	}
	format, ok := body["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		return resp, ""
	}
	schemaContainer, ok := format["json_schema"].(map[string]any)
	if !ok || schemaContainer["schema"] == nil {
		return resp, ""
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if readErr != nil {
		resp.Body.Close()
		return nil, "transport_error"
	}
	resp.Body = &readCloser{Reader: io.MultiReader(bytes.NewReader(prefix), resp.Body), Closer: resp.Body}
	if len(prefix) > 4096 || !unsupportedStructuredOutput(prefix) {
		return resp, ""
	}
	schema, err := json.Marshal(schemaContainer["schema"])
	if err != nil {
		resp.Body.Close()
		return nil, "invalid_request"
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		resp.Body.Close()
		return nil, "invalid_request"
	}
	body["messages"] = append(messages, map[string]any{
		"role":    "system",
		"content": "Return only a JSON object that conforms to this schema: " + string(schema),
	})
	delete(body, "response_format")
	payload, err = json.Marshal(body)
	if err != nil || len(payload) > maxRequestBytes {
		resp.Body.Close()
		return nil, "invalid_request"
	}
	resp.Body.Close()
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, "invalid_request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = o.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "deadline_exceeded"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, "cancelled"
		}
		return nil, "transport_error"
	}
	return resp, ""
}

type readCloser struct {
	io.Reader
	io.Closer
}

func unsupportedStructuredOutput(data []byte) bool {
	const message = "structured output is unavailable"
	if strings.TrimSpace(string(data)) == message {
		return true
	}
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return false
	}
	var text string
	if json.Unmarshal(body.Error, &text) == nil {
		return text == message
	}
	var nested struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body.Error, &nested) == nil && nested.Message == message
}

func validProviderInput(body map[string]any, operation string) bool {
	switch operation {
	case "chat":
		if _, hasMax := body["max_tokens"]; !hasMax {
			if _, hasCompletion := body["max_completion_tokens"]; !hasCompletion {
				return false
			}
		}
		if _, exists := body["max_output_tokens"]; exists {
			return false
		}
	case "responses":
		if _, exists := body["max_output_tokens"]; !exists {
			return false
		}
		if _, exists := body["max_tokens"]; exists {
			return false
		}
		if _, exists := body["max_completion_tokens"]; exists {
			return false
		}
	default:
		return false
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := body[key]; ok {
			num, ok := value.(json.Number)
			if !ok {
				return false
			}
			n, err := num.Int64()
			if err != nil || n < 1 || n > 32768 {
				return false
			}
		}
	}
	if tools, ok := body["tools"]; ok {
		list, ok := tools.([]any)
		if !ok || len(list) > 128 {
			return false
		}
		for _, entry := range list {
			tool, ok := entry.(map[string]any)
			if !ok || tool["type"] != "function" {
				return false
			}
		}
	}
	return safeMedia(body["messages"]) && safeMedia(body["input"])
}

func safeMedia(value any) bool {
	switch item := value.(type) {
	case []any:
		for _, entry := range item {
			if !safeMedia(entry) {
				return false
			}
		}
	case map[string]any:
		if kind, _ := item["type"].(string); kind == "input_file" || kind == "file" || kind == "input_audio" || kind == "audio_url" || kind == "video_url" {
			return false
		}
		for key, entry := range item {
			if key == "image_url" {
				switch image := entry.(type) {
				case string:
					if !strings.HasPrefix(image, "data:image/") {
						return false
					}
				case map[string]any:
					url, ok := image["url"].(string)
					if !ok || !strings.HasPrefix(url, "data:image/") {
						return false
					}
				default:
					return false
				}
			}
			if !safeMedia(entry) {
				return false
			}
		}
	}
	return true
}
