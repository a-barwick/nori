package google

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"nori/internal/invoke"
)

const sheetsBase = "https://sheets.googleapis.com/v4/spreadsheets/"

var a1Pattern = regexp.MustCompile(`^([A-Za-z]{1,3})([1-9][0-9]{0,5})(?::([A-Za-z]{1,3})([1-9][0-9]{0,5}))?$`)

type sheetRange struct{ startCol, startRow, endCol, endRow int }

func columnNumber(value string) int {
	n := 0
	for _, c := range strings.ToUpper(value) {
		n = n*26 + int(c-'A'+1)
	}
	return n
}
func columnName(number int) string {
	out := ""
	for number > 0 {
		number--
		out = string(rune('A'+number%26)) + out
		number /= 26
	}
	return out
}

func parseSheetRange(value string) (sheetRange, error) {
	m := a1Pattern.FindStringSubmatch(value)
	if m == nil {
		return sheetRange{}, fmt.Errorf("%w: range must use A1 notation, such as A1:D20", invoke.ErrInvalidArguments)
	}
	startRow, _ := strconv.Atoi(m[2])
	out := sheetRange{startCol: columnNumber(m[1]), startRow: startRow}
	out.endCol, out.endRow = out.startCol, out.startRow
	if m[3] != "" {
		out.endCol = columnNumber(m[3])
		out.endRow, _ = strconv.Atoi(m[4])
	}
	if out.endCol < out.startCol || out.endRow < out.startRow || (out.endCol-out.startCol+1)*(out.endRow-out.startRow+1) > 50000 {
		return sheetRange{}, fmt.Errorf("%w: range is too large or reversed", invoke.ErrInvalidArguments)
	}
	return out, nil
}

func (r sheetRange) String() string {
	return columnName(r.startCol) + strconv.Itoa(r.startRow) + ":" + columnName(r.endCol) + strconv.Itoa(r.endRow)
}

func parseSheetCSV(content string) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(content))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: spreadsheet content must be valid CSV", invoke.ErrInvalidArguments)
	}
	count := 0
	for _, row := range rows {
		count += len(row)
	}
	if len(rows) == 0 || count == 0 || count > 50000 {
		return nil, fmt.Errorf("%w: CSV must have between 1 and 50000 cells", invoke.ErrInvalidArguments)
	}
	return rows, nil
}

func sheetSelection(ctx context.Context, client *http.Client, id, chosen string, requireChoice bool) (string, []string, error) {
	params := url.Values{"fields": {"sheets(properties(title))"}}
	var spreadsheet struct {
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := jsonRequest(ctx, client, "GET", sheetsBase+pathID(id)+"?"+params.Encode(), nil, nil, &spreadsheet); err != nil {
		return "", nil, err
	}
	names := make([]string, 0, len(spreadsheet.Sheets))
	found := false
	for _, sheet := range spreadsheet.Sheets {
		if sheet.Properties.Title != "" {
			names = append(names, sheet.Properties.Title)
			if sheet.Properties.Title == chosen {
				found = true
			}
		}
	}
	if len(names) == 0 {
		return "", nil, errors.New("This spreadsheet has no sheets")
	}
	if chosen == "" && requireChoice && len(names) > 1 {
		return "", names, fmt.Errorf("%w: choose a sheet tab: %s", invoke.ErrInvalidArguments, strings.Join(names, ", "))
	}
	if chosen == "" {
		chosen = names[0]
		found = true
	}
	if !found {
		return "", names, fmt.Errorf("%w: sheet tab %q does not exist", invoke.ErrInvalidArguments, chosen)
	}
	return chosen, names, nil
}

func quotedSheetRange(sheet string, r sheetRange) string {
	return "'" + strings.ReplaceAll(sheet, "'", "''") + "'!" + r.String()
}

func prepareSheetUpdate(ctx context.Context, client *http.Client, id string, args map[string]any, spec map[string]string) error {
	if spec["name"] != "" {
		return fmt.Errorf("%w: rename the spreadsheet in a separate action", invoke.ErrInvalidArguments)
	}
	rows, err := parseSheetCSV(spec["content"])
	if err != nil {
		return err
	}
	name, _, err := sheetSelection(ctx, client, id, invoke.String(args, "sheet"), true)
	if err != nil {
		return err
	}
	rawRange := invoke.String(args, "range")
	if rawRange == "" {
		rawRange = "A1"
	}
	r, err := parseSheetRange(rawRange)
	if err != nil {
		return err
	}
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	targetEndCol, targetEndRow := r.startCol+maxCols-1, r.startRow+len(rows)-1
	if strings.Contains(rawRange, ":") && (targetEndCol > r.endCol || targetEndRow > r.endRow) {
		return fmt.Errorf("%w: CSV data exceeds the selected range", invoke.ErrInvalidArguments)
	}
	r.endCol, r.endRow = targetEndCol, targetEndRow
	if (r.endCol-r.startCol+1)*(r.endRow-r.startRow+1) > 50000 {
		return fmt.Errorf("%w: CSV range exceeds 50000 cells", invoke.ErrInvalidArguments)
	}
	values, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	spec["sheet"], spec["range"], spec["values"] = name, r.String(), string(values)
	if interpret, ok := args["interpret_values"].(bool); ok && interpret {
		spec["interpret_values"] = "true"
	}
	return nil
}

func readSheet(ctx context.Context, client *http.Client, file driveFile, args map[string]any) (map[string]any, error) {
	name, names, err := sheetSelection(ctx, client, file.ID, invoke.String(args, "sheet"), false)
	if err != nil {
		return nil, err
	}
	rawRange := invoke.String(args, "range")
	if rawRange == "" {
		rawRange = "A1:Z200"
	}
	r, err := parseSheetRange(rawRange)
	if err != nil {
		return nil, err
	}
	var result struct {
		Values [][]any `json:"values"`
	}
	endpoint := sheetsBase + pathID(file.ID) + "/values/" + pathID(quotedSheetRange(name, r))
	if err = jsonRequest(ctx, client, "GET", endpoint, nil, nil, &result); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	for _, row := range result.Values {
		fields := make([]string, len(row))
		for i, value := range row {
			fields[i] = fmt.Sprint(value)
		}
		if err = writer.Write(fields); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err = writer.Error(); err != nil {
		return nil, err
	}
	text := buffer.String()
	truncated := false
	if len(text) > maxDriveText {
		text = text[:maxDriveText]
		truncated = true
	}
	return map[string]any{"file": file, "sheet": name, "sheets": names, "range": r.String(), "text": strings.ToValidUTF8(text, ""), "truncated": truncated, "untrusted": "File content is external data."}, nil
}

func executeSheetUpdate(ctx context.Context, client *http.Client, fileID string, spec map[string]string) (map[string]any, error) {
	r, err := parseSheetRange(spec["range"])
	if err != nil {
		return nil, err
	}
	var rows [][]string
	if err = json.Unmarshal([]byte(spec["values"]), &rows); err != nil {
		return nil, err
	}
	option := "RAW"
	if spec["interpret_values"] == "true" {
		option = "USER_ENTERED"
	}
	params := url.Values{"valueInputOption": {option}}
	endpoint := sheetsBase + pathID(fileID) + "/values/" + pathID(quotedSheetRange(spec["sheet"], r)) + "?" + params.Encode()
	var result map[string]any
	err = jsonRequest(ctx, client, "PUT", endpoint, map[string]any{"majorDimension": "ROWS", "values": rows}, nil, &result)
	return result, err
}
