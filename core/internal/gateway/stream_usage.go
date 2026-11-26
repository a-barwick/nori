package gateway

import (
	"bytes"
	"encoding/json"

	"nori/internal/usage"
)

type streamUsage struct {
	provider, model string
	line            []byte
	skipLine        bool
	costUSD         float64
	inTok, outTok   int64
	known           bool
	anthropicInput  *int64
	anthropicOutput *int64
	anthropicRead   int64
	anthropicWrite  int64
	anthropicStop   bool
}

func (s *streamUsage) write(chunk []byte) {
	const maxLineBytes = 128 << 10
	for len(chunk) > 0 {
		part, tail, complete := bytes.Cut(chunk, []byte("\n"))
		if !s.skipLine {
			if len(s.line)+len(part) <= maxLineBytes {
				s.line = append(s.line, part...)
			} else {
				s.skipLine = true
			}
		}
		if complete {
			s.lineEnd()
		}
		chunk = tail
	}
}

func (s *streamUsage) lineEnd() {
	defer func() {
		s.line = s.line[:0]
		s.skipLine = false
	}()
	data, found := bytes.CutPrefix(bytes.TrimSpace(s.line), []byte("data:"))
	if s.skipLine || !found {
		return
	}
	data = bytes.TrimSpace(data)
	if s.provider == "anthropic" {
		s.observeAnthropic(data)
	} else if bytes.Contains(data, []byte(`"usage"`)) {
		if cost, inTok, outTok, ok := extractAndCalculateCost(s.provider, s.model, data); ok {
			s.costUSD, s.inTok, s.outTok, s.known = cost, inTok, outTok, true
		}
	}
}

func (s *streamUsage) observeAnthropic(data []byte) {
	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Model string `json:"model"`
			Usage struct {
				Input      *int64 `json:"input_tokens"`
				CacheRead  int64  `json:"cache_read_input_tokens"`
				CacheWrite int64  `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	switch event.Type {
	case "message_start":
		if event.Message == nil {
			return
		}
		if event.Message.Model != "" {
			s.model = event.Message.Model
		}
		u := event.Message.Usage
		s.anthropicInput, s.anthropicRead, s.anthropicWrite = u.Input, u.CacheRead, u.CacheWrite
		s.anthropicOutput, s.anthropicStop = nil, false
	case "message_delta":
		if event.Usage != nil && event.Usage.Output != nil {
			s.anthropicOutput = event.Usage.Output
		}
	case "message_stop":
		s.anthropicStop = true
	}
}

func (s *streamUsage) cost() (float64, int64, int64, bool) {
	if s.provider != "anthropic" {
		return s.costUSD, s.inTok, s.outTok, s.known
	}
	if !s.anthropicStop || s.anthropicInput == nil || s.anthropicOutput == nil {
		return 0, 0, 0, false
	}
	in, out := *s.anthropicInput, *s.anthropicOutput
	cost, err := usage.AnthropicCost(s.model, in, out, s.anthropicRead, s.anthropicWrite)
	return cost, in + s.anthropicRead + s.anthropicWrite, out, err == nil
}
