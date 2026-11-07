package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"nori/internal/state/docstore"
)

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*$`)

func expression(field string) (string, error) {
	if field == docstore.DocumentID {
		return "to_jsonb(id)", nil
	}
	if !fieldName.MatchString(field) {
		return "", fmt.Errorf("invalid document field")
	}
	if !strings.Contains(field, ".") {
		return "data->'" + field + "'", nil
	}
	return "data#>'{" + strings.ReplaceAll(field, ".", ",") + "}'", nil
}
func query(ctx context.Context, q executor, s docstore.QuerySpec) ([]docstore.Record, error) {
	args := []any{s.Collection}
	clauses := []string{"collection=$1"}
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
		field, e := expression(f.Field)
		if e != nil {
			return nil, e
		}
		v, e := value(f.Value)
		if e != nil {
			return nil, e
		}
		switch f.Op {
		case "==":
			clauses = append(clauses, field+" = "+v)
		case ">", ">=", "<", "<=":
			clauses = append(clauses, field+" "+f.Op+" "+v)
		case "in":
			clauses = append(clauses, field+" IN (SELECT jsonb_array_elements("+v+"))")
		default:
			return nil, fmt.Errorf("unsupported document comparison")
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
			return nil, e
		}
		direction, op := "ASC", ">"
		if order.Direction == docstore.Desc {
			direction, op = "DESC", "<"
		}
		ordered = append(ordered, field+" "+direction)
		// Firestore ordering excludes records where that field is absent.
		if order.Field != docstore.DocumentID {
			clauses = append(clauses, field+" IS NOT NULL")
		}
		if s.After != nil {
			raw := s.After.Fields[order.Field]
			if order.Field == docstore.DocumentID {
				_, rawID, e := split(s.After.Path)
				if e != nil {
					return nil, e
				}
				raw = rawID
			}
			v, e := value(raw)
			if e != nil {
				return nil, e
			}
			part := append(append([]string{}, prefix...), field+" "+op+" "+v)
			after = append(after, "("+strings.Join(part, " AND ")+")")
			prefix = append(prefix, field+" = "+v)
		}
	}
	if len(after) > 0 {
		clauses = append(clauses, "("+strings.Join(after, " OR ")+")")
	}
	sql := "SELECT id,data FROM nori_records WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + strings.Join(ordered, ",")
	if s.Count > 0 {
		args = append(args, s.Count)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, e := q.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []docstore.Record{}
	for rows.Next() {
		var id string
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
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
		result = append(result, docstore.Record{Path: s.Collection + "/" + id, Fields: fields})
	}
	return result, rows.Err()
}
