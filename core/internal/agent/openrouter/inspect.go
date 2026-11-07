package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"nori/internal/agent"
	"nori/internal/usage"
)

// Inspect sends one image and one question in a request of its own with no tools.
func (c *Client) Inspect(ctx context.Context, req agent.InspectRequest) (out agent.Inspection, err error) {
	if c.ModelName != selectedModel {
		return out, errors.New("task model must be the selected openai/gpt-6.1-terra")
	}
	image := "data:" + req.MediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Image)
	body, err := json.Marshal(map[string]any{
		"model": c.ModelName, "instructions": req.Instructions, "max_output_tokens": req.MaxOutputTokens, "store": false,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": req.Question},
			map[string]any{"type": "input_image", "image_url": image, "detail": "high"},
		}}},
		"provider": routing(),
	})
	if err != nil {
		return out, errors.New("inspection request could not be encoded")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	httpReq.Header.Set("Content-Type", "application/json")
	record := usage.Attempt(ctx, provider, c.ModelName)
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() {
		if out.CostKnown {
			record.CostUSD, record.CostStatus = &out.Cost, "provider"
		}
		err = errors.Join(err, usage.Finish(ctx, c.Meter, record))
	}()
	out.Sent = true
	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, errors.New("inspection request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("inspection model returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *struct {
			Cost   *float64 `json:"cost"`
			Input  *int64   `json:"input_tokens"`
			Output *int64   `json:"output_tokens"`
			Total  *int64   `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return out, errors.New("inspection model returned an unreadable response")
	}
	if u := response.Usage; u != nil {
		record.InputTokens, record.OutputTokens, record.TotalTokens = u.Input, u.Output, u.Total
	}
	if response.Usage != nil && response.Usage.Cost != nil && *response.Usage.Cost >= 0 &&
		!math.IsNaN(*response.Usage.Cost) && !math.IsInf(*response.Usage.Cost, 0) {
		out.Cost, out.CostKnown = *response.Usage.Cost, true
	}
	text := []string{}
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text = append(text, content.Text)
			}
		}
	}
	out.Observation = strings.TrimSpace(strings.Join(text, "\n"))
	if response.Status != "completed" {
		out.Observation = ""
	}
	return out, nil
}
