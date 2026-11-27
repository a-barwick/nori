package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"nori/internal/state/docstore"
)

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*$`)

var indexedColumns = map[string]string{
	"seq":                 "idx_seq",
	"created_at":          "idx_created_at",
	"started_at":          "idx_started_at",
	"position":            "idx_position",
	"status":              "idx_status",
	"run_id":              "idx_run_id",
	"delivered":           "idx_delivered",
	"cleanup_pending":     "idx_cleanup_pending",
	"publication_pending": "idx_publication_pending",
	"email":               "idx_email",
	"index.status":        "idx_index_status",
	"index.task_id":       "idx_index_task_id",
}

func expression(field string) (string, error) {
	if field == docstore.DocumentID {
		return "id", nil
	}
	if col, ok := indexedColumns[field]; ok {
		return col, nil
	}
	if !fieldName.MatchString(field) {
		return "", fmt.Errorf("invalid document field")
	}
	if !strings.Contains(field, ".") {
		return "data->'" + field + "'", nil
	}
	return "data#>'{" + strings.ReplaceAll(field, ".", ",") + "}'", nil
}
func lookupField(fields map[string]any, field string) (any, bool) {
	if fields == nil {
		return nil, false
	}
	if v, ok := fields[field]; ok {
		return v, true
	}
	if !strings.Contains(field, ".") {
		return nil, false
	}
	parts := strings.Split(field, ".")
	cur := fields
	for i, part := range parts {
		v, ok := cur[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false
}
func buildQuery(s docstore.QuerySpec) (string, []any, bool, error) {
	multi := len(s.Collections) > 0
	var args []any
	var clauses []string
	if multi {
		shard := recordShard(s.Collections[0])
		for _, col := range s.Collections[1:] {
			if recordShard(col) != shard {
				return "", nil, false, fmt.Errorf("multi-collection query must share a single shard")
			}
		}
		args = []any{shard, s.Collections}
		clauses = []string{"shard=$1", "collection = ANY($2::text[])"}
	} else {
		args = []any{recordShard(s.Collection), s.Collection}
		clauses = []string{"shard=$1", "collection=$2"}
	}
	value := func(v any) (string, error) {
		x, e := docstore.Value(v)
		if e != nil {
			return "", e
		}
		raw, e := json.Marshal(docstore.JSONValue(x))
		if e != nil {
			return "", e
		}
		args = append(args, string(raw))
		return fmt.Sprintf("$%d::jsonb", len(args)), nil
	}
	for _, f := range s.Filters {
		if f.Field == docstore.DocumentID {
			x, e := docstore.Value(f.Value)
			if e != nil {
				return "", nil, false, e
			}
			switch f.Op {
			case "==", ">", ">=", "<", "<=":
				if str, ok := x.(string); ok {
					args = append(args, str)
					op := f.Op
					if op == "==" {
						op = "="
					}
					clauses = append(clauses, fmt.Sprintf("id %s $%d", op, len(args)))
					continue
				}
			case "in":
				if arr, ok := x.([]any); ok {
					strs := make([]string, 0, len(arr))
					allStrings := true
					for _, elem := range arr {
						str, ok := elem.(string)
						if !ok {
							allStrings = false
							break
						}
						strs = append(strs, str)
					}
					if allStrings {
						args = append(args, strs)
						clauses = append(clauses, fmt.Sprintf("id = ANY($%d::text[])", len(args)))
						continue
					}
				}
			default:
				return "", nil, false, fmt.Errorf("unsupported document comparison")
			}
			v, e := value(f.Value)
			if e != nil {
				return "", nil, false, e
			}
			switch f.Op {
			case "==":
				clauses = append(clauses, "to_jsonb(id) = "+v)
			case ">", ">=", "<", "<=":
				clauses = append(clauses, "to_jsonb(id) "+f.Op+" "+v)
			case "in":
				clauses = append(clauses, "to_jsonb(id) IN (SELECT jsonb_array_elements("+v+"))")
			}
			continue
		}
		field, e := expression(f.Field)
		if e != nil {
			return "", nil, false, e
		}
		v, e := value(f.Value)
		if e != nil {
			return "", nil, false, e
		}
		switch f.Op {
		case "==":
			clauses = append(clauses, field+" = "+v)
			if f.Field == "run_id" && !multi {
				if strings.HasSuffix(s.Collection, "/results") {
					clauses = append(clauses, "right(collection, 8) = '/results'")
				} else {
					clauses = append(clauses, "right(collection, 8) <> '/results'")
				}
			}
		case ">", ">=", "<", "<=":
			clauses = append(clauses, field+" "+f.Op+" "+v)
		case "in":
			clauses = append(clauses, field+" IS NOT NULL", field+" IN (SELECT jsonb_array_elements("+v+"))")
		default:
			return "", nil, false, fmt.Errorf("unsupported document comparison")
		}
	}
	orders := append([]docstore.Order{}, s.Orders...)
	if len(orders) == 0 {
		orders = append(orders, docstore.Order{Field: docstore.DocumentID, Direction: docstore.Asc})
	}
	if orders[len(orders)-1].Field != docstore.DocumentID {
		orders = append(orders, docstore.Order{Field: docstore.DocumentID, Direction: orders[len(orders)-1].Direction})
	}
	ordered := []string{}
	prefix := []string{}
	after := []string{}
	for _, order := range orders {
		field, e := expression(order.Field)
		if e != nil {
			return "", nil, false, e
		}
		direction, op := "ASC", ">"
		if order.Direction == docstore.Desc {
			direction, op = "DESC", "<"
		}
		ordered = append(ordered, field+" "+direction)
		if order.Field != docstore.DocumentID {
			clauses = append(clauses, field+" IS NOT NULL")
		}
		if s.After != nil {
			var v string
			if order.Field == docstore.DocumentID {
				_, rawID, e := split(s.After.Path)
				if e != nil {
					return "", nil, false, e
				}
				args = append(args, rawID)
				v = fmt.Sprintf("$%d", len(args))
			} else {
				raw, _ := lookupField(s.After.Fields, order.Field)
				v, e = value(raw)
				if e != nil {
					return "", nil, false, e
				}
			}
			part := append(append([]string{}, prefix...), field+" "+op+" "+v)
			after = append(after, "("+strings.Join(part, " AND ")+")")
			prefix = append(prefix, field+" = "+v)
		}
	}
	if len(after) > 0 {
		clauses = append(clauses, "("+strings.Join(after, " OR ")+")")
	}
	selectCols := "id,data"
	if multi {
		selectCols = "collection,id,data"
	}
	sql := "SELECT " + selectCols + " FROM nori_records WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + strings.Join(ordered, ",")
	if s.Count > 0 {
		args = append(args, s.Count)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return sql, args, multi, nil
}

func scanQueryRows(rows pgx.Rows, s docstore.QuerySpec, multi bool) ([]docstore.Record, error) {
	defer rows.Close()
	result := []docstore.Record{}
	for rows.Next() {
		col := s.Collection
		var id string
		var raw []byte
		var e error
		if multi {
			e = rows.Scan(&col, &id, &raw)
		} else {
			e = rows.Scan(&id, &raw)
		}
		if e != nil {
			return nil, e
		}
		fields, e := decode(raw)
		if e != nil {
			return nil, e
		}
		if s.Fields != nil {
			selected := map[string]any{}
			for _, name := range s.Fields {
				if v, ok := fields[name]; ok {
					selected[name] = v
				}
			}
			fields = selected
		}
		result = append(result, docstore.Record{Path: col + "/" + id, Fields: fields})
	}
	return result, rows.Err()
}

func query(ctx context.Context, q executor, s docstore.QuerySpec) ([]docstore.Record, error) {
	sql, args, multi, e := buildQuery(s)
	if e != nil {
		return nil, e
	}
	rows, e := q.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	return scanQueryRows(rows, s, multi)
}
