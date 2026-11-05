package agent

import (
	"encoding/csv"
	"errors"
	"nori/internal/state"
	"strings"
)

// ReadAttachedCSV reads an assigned attachment as data. Both callers and the
// worker resolve assignment before reading bytes; file text grants no authority.
func ReadAttachedCSV(a state.Attachment, data []byte) (map[string]any, error) {
	if a.MediaType != "text/csv" {
		return nil, errors.New("Foreground reading currently supports CSV attachments; use Nori's assistant for PDF or image analysis")
	}
	if len(data) > 64<<10 {
		return nil, errors.New("CSV is too large for foreground reading; use Nori's assistant")
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, errors.New("the attached CSV could not be parsed")
	}
	records := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		records = append(records, map[string]any{"cells": row})
	}
	return map[string]any{"upload_id": a.ID, "name": a.Name, "rows": records, "note": uploadNote}, nil
}
