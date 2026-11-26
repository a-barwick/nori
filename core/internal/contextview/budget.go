package contextview

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const (
	ReplyBytes = 96 << 10
	TaskBytes  = 128 << 10
	WireBytes  = 192 << 10
)

var ErrOverflow = errors.New("model context exceeds its input budget")

func Check(size int) error {
	if size > WireBytes {
		return ErrOverflow
	}
	return nil
}

var overflowMessage = regexp.MustCompile(`(?i)(maximum context length is [0-9,]+ tokens|exceeds (the )?(model.s )?(maximum context length|context window)|prompt is too long|input is too long for requested model)`)

func Refusal(body io.Reader) bool {
	var response struct {
		Error struct {
			Code     json.RawMessage `json:"code"`
			Type     string          `json:"type"`
			Message  string          `json:"message"`
			Metadata struct {
				Raw string `json:"raw"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(body, 16<<10)).Decode(&response) != nil {
		return false
	}
	var code string
	_ = json.Unmarshal(response.Error.Code, &code)
	for _, value := range []string{code, response.Error.Type} {
		switch value {
		case "context_length_exceeded", "context_window_exceeded", "input_too_long":
			return true
		}
	}
	return overflowMessage.MatchString(response.Error.Message) || overflowMessage.MatchString(response.Error.Metadata.Raw)
}
