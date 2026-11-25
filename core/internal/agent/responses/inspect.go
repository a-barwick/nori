package responses

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nori/internal/agent"
	"nori/internal/modelgateway"
	"nori/internal/usage"
)

func (c *Client) Inspect(ctx context.Context, req agent.InspectRequest) (out agent.Inspection, err error) {
	if !c.validModel() {
		return out, errors.New("task model must be the selected GPT-6.1 Terra")
	}
	image := "data:" + req.MediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Image)
	fields := map[string]any{
		"model": c.ModelName, "instructions": req.Instructions, "max_output_tokens": req.MaxOutputTokens, "store": false,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": req.Question},
			map[string]any{"type": "input_image", "image_url": image, "detail": "high"},
		}}},
	}
	c.requestOptions(fields)
	body, err := json.Marshal(fields)
	if err != nil {
		return out, errors.New("inspection request could not be encoded")
	}
	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		return httpReq, nil
	}
	if _, err := buildReq(); err != nil {
		return out, err
	}
	record := usage.Attempt(ctx, c.Provider(), c.ModelName)
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() { err = errors.Join(err, usage.Finish(ctx, c.Meter, record)) }()
	out.Sent = true
	res, err := c.doWithRetry(ctx, buildReq)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("inspection request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if c.gatewayURL != "" {
			return out, modelgateway.HTTPError(res.StatusCode)
		}
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
		Model string         `json:"model"`
		Tier  string         `json:"service_tier"`
		Usage *responseUsage `json:"usage"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return out, errors.New("inspection model returned an unreadable response")
	}
	out.Cost, out.CostKnown = c.recordCost(&record, response.Model, response.Tier, response.Usage)
	if out.CostKnown && out.Cost > 0 {
		c.guard().RecordActualCost(out.Cost)
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
