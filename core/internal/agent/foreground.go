package agent

import (
	"encoding/csv"
	"errors"
	"nori/internal/prompts"
	"nori/internal/state"
	"strings"
)

// ReadAttachedCSV reads an assigned attachment as data. Both callers and the
// worker resolve assignment before reading bytes; file text grants no authority.
func ReadAttachedCSV(a state.Attachment, data []byte) (map[string]any, error) {
	if a.MediaType != "text/csv" {
		return nil, errors.New(prompts.Runtime("tool-foreground-reading-currently-supports-csv-attachments-use-nori-s", nil))
	}
	if len(data) > 64<<10 {
		return nil, errors.New(prompts.Runtime("tool-csv-is-too-large-for-foreground-reading-use-nori-s-assistant", nil))
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, errors.New(prompts.Runtime("tool-the-attached-csv-could-not-be-parsed", nil))
	}
	records := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		records = append(records, map[string]any{"cells": row})
	}
	return map[string]any{"upload_id": a.ID, "name": a.Name, "rows": records, "note": uploadNote}, nil
}

// ReadAttached reads a text attachment in the foreground: CSV as rows, other text (calendar files, plain text,
// Markdown) as text, so a side chat reads the same files a task reads with upload_read_text.
func ReadAttached(a state.Attachment, data []byte) (map[string]any, error) {
	if a.MediaType == "text/csv" {
		return ReadAttachedCSV(a, data)
	}
	if !strings.HasPrefix(a.MediaType, "text/") {
		return ReadAttachedCSV(a, data) // explains which files can be read here
	}
	const limit = 64 << 10
	text := data
	truncated := len(text) > limit
	if truncated {
		text = text[:limit]
	}
	out := map[string]any{"upload_id": a.ID, "name": a.Name, "media_type": a.MediaType, "text": strings.ToValidUTF8(string(text), ""), "note": uploadNote}
	if truncated {
		out["truncated"] = true
	}
	if a.MediaType == "text/calendar" {
		out["calendar_note"] = "This is an uploaded calendar file. Checking it against a calendar or importing it runs as a task with the calendar tools."
	}
	return out, nil
}
