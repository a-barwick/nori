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

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/spend"
)

// A provider bills an image by its dimensions, not its encoded size. The spend
// reservation counts one token per byte, so it stands in this fixed ceiling for
// the image, which is several times a full-page screenshot's token count.
const inspectImageTokens = 20_000

// Inspect sends one image and one question in a request of its own with no tools.
func (c *Client) Inspect(ctx context.Context, req agent.InspectRequest) (out agent.Inspection, err error) {
	if c.ModelName != selectedModel {
		return out, errors.New("task model must be the selected openai/gpt-6.1-terra")
	}
	control, limit, err := spend.Prepare(ctx, provider, req.MaxOutputTokens)
	if err != nil {
		return out, err
	}
	defer control.Close()
	image := "data:" + req.MediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Image)
	body, err := json.Marshal(map[string]any{
		"model": c.ModelName, "instructions": req.Instructions, "max_output_tokens": limit, "store": false,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": req.Question},
			map[string]any{"type": "input_image", "image_url": image, "detail": "high"},
		}}},
		"provider": routing(),
	})
	if err != nil {
		return out, errors.New("inspection request could not be encoded")
	}
	reservation, err := control.Reserve(ctx, provider, c.ModelName, len(body)-len(image)+inspectImageTokens, limit, uuid.NewString())
	if err != nil {
		return out, err
	}
	if reservation != nil {
		defer func() {
			var cost *float64
			if out.CostKnown {
				cost = &out.Cost
			}
			err = errors.Join(err, reservation.Settle(ctx, cost, "", nil))
		}()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	httpReq.Header.Set("Content-Type", "application/json")
	out.Sent = true
	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, errors.New("inspection request interrupted or unavailable")
	}
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
			Cost *float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return out, errors.New("inspection model returned an unreadable response")
	}
	if response.Usage == nil || response.Usage.Cost == nil || *response.Usage.Cost < 0 ||
		math.IsNaN(*response.Usage.Cost) || math.IsInf(*response.Usage.Cost, 0) {
		return out, errors.New("inspection model reported no cost; budget cannot be enforced")
	}
	out.Cost, out.CostKnown = *response.Usage.Cost, true
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
