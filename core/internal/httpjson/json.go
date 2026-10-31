// Package httpjson decodes bounded HTTP request objects.
package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var ErrObject = errors.New("invalid JSON object")
var ErrMultiple = errors.New("one JSON object required")

func Decode(w http.ResponseWriter, r *http.Request, value any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return ErrObject
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrMultiple
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return ErrObject
	}
	body := json.NewDecoder(bytes.NewReader(raw))
	body.DisallowUnknownFields()
	if err := body.Decode(value); err != nil {
		return ErrObject
	}
	return nil
}

func Write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
