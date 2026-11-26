package memstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"nori/internal/state/docstore"
)

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*$`)

func validateField(field string) error {
	if field == docstore.DocumentID {
		return nil
	}
	if !fieldName.MatchString(field) {
		return fmt.Errorf("invalid document field")
	}
	return nil
}

func normalizeValue(v any) (any, error) {
	x, err := docstore.Value(v)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(docstore.JSONValue(x))
	if err != nil {
		return nil, err
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return docstore.NormalizeNumbers(out), nil
}

func lookupField(fields map[string]any, id, field string) (any, bool) {
	if field == docstore.DocumentID {
		return id, true
	}
	if fields == nil {
		return nil, false
	}
	if !strings.Contains(field, ".") {
		v, ok := fields[field]
		return v, ok
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

func jsonbTypeRank(v any) int {
	switch v.(type) {
	case nil:
		return 0
	case string:
		return 1
	case int64, float64, json.Number:
		return 2
	case bool:
		return 3
	case []any:
		return 4
	case map[string]any:
		return 5
	default:
		return 0
	}
}

func compareNumbers(a, b any) int {
	ai, aIsInt := a.(int64)
	bi, bIsInt := b.(int64)
	if aIsInt && bIsInt {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	af, aIsFloat := a.(float64)
	bf, bIsFloat := b.(float64)
	if aIsFloat && bIsFloat {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	ba := toBigFloat(a)
	bb := toBigFloat(b)
	return ba.Cmp(bb)
}

func toBigFloat(v any) *big.Float {
	switch x := v.(type) {
	case int64:
		return new(big.Float).SetPrec(128).SetInt64(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return new(big.Float).SetPrec(128)
		}
		return new(big.Float).SetPrec(128).SetFloat64(x)
	case json.Number:
		f, _, _ := new(big.Float).SetPrec(128).Parse(string(x), 10)
		if f != nil {
			return f
		}
	}
	return new(big.Float).SetPrec(128)
}

func compareJSONBKey(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func compareJSONB(a, b any) int {
	ra, rb := jsonbTypeRank(a), jsonbTypeRank(b)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	switch ra {
	case 0:
		return 0
	case 1:
		return strings.Compare(a.(string), b.(string))
	case 2:
		return compareNumbers(a, b)
	case 3:
		ab, bb := a.(bool), b.(bool)
		if ab == bb {
			return 0
		}
		if !ab && bb {
			return -1
		}
		return 1
	case 4:
		aa, ba := a.([]any), b.([]any)
		if len(aa) != len(ba) {
			if len(aa) < len(ba) {
				return -1
			}
			return 1
		}
		for i := range aa {
			if c := compareJSONB(aa[i], ba[i]); c != 0 {
				return c
			}
		}
		return 0
	case 5:
		am, bm := a.(map[string]any), b.(map[string]any)
		if len(am) != len(bm) {
			if len(am) < len(bm) {
				return -1
			}
			return 1
		}
		ak := make([]string, 0, len(am))
		for k := range am {
			ak = append(ak, k)
		}
		bk := make([]string, 0, len(bm))
		for k := range bm {
			bk = append(bk, k)
		}
		slices.SortFunc(ak, compareJSONBKey)
		slices.SortFunc(bk, compareJSONBKey)
		for i := range ak {
			if c := compareJSONBKey(ak[i], bk[i]); c != 0 {
				return c
			}
			if c := compareJSONB(am[ak[i]], bm[bk[i]]); c != 0 {
				return c
			}
		}
		return 0
	default:
		return 0
	}
}

type compiledFilter struct {
	field string
	op    string
	val   any
	inSet []any
}

type queryRow struct {
	id     string
	fields map[string]any
}

func executeQuery(docs map[string][]byte, s docstore.QuerySpec) ([]docstore.Record, error) {
	filters := make([]compiledFilter, 0, len(s.Filters))
	for _, f := range s.Filters {
		if err := validateField(f.Field); err != nil {
			return nil, err
		}
		norm, err := normalizeValue(f.Value)
		if err != nil {
			return nil, err
		}
		cf := compiledFilter{field: f.Field, op: f.Op, val: norm}
		switch f.Op {
		case "==", ">", ">=", "<", "<=":
		case "in":
			arr, ok := norm.([]any)
			if !ok {
				return nil, fmt.Errorf("in filter requires an array")
			}
			cf.inSet = arr
		default:
			return nil, fmt.Errorf("unsupported document comparison")
		}
		filters = append(filters, cf)
	}

	orders := append([]docstore.Order{}, s.Orders...)
	if len(orders) == 0 {
		orders = append(orders, docstore.Order{Field: docstore.DocumentID, Direction: docstore.Asc})
	}
	if orders[len(orders)-1].Field != docstore.DocumentID {
		orders = append(orders, docstore.Order{
			Field:     docstore.DocumentID,
			Direction: orders[len(orders)-1].Direction,
		})
	}
	for _, o := range orders {
		if err := validateField(o.Field); err != nil {
			return nil, err
		}
	}

	var afterVals []any
	if s.After != nil {
		afterVals = make([]any, len(orders))
		for i, o := range orders {
			var raw any
			if o.Field == docstore.DocumentID {
				_, rawID, err := split(s.After.Path)
				if err != nil {
					return nil, err
				}
				raw = rawID
			} else if s.After.Fields != nil {
				if v, ok := s.After.Fields[o.Field]; ok {
					raw = v
				} else if v, ok := lookupField(s.After.Fields, "", o.Field); ok {
					raw = v
				}
			}
			norm, err := normalizeValue(raw)
			if err != nil {
				return nil, err
			}
			afterVals[i] = norm
		}
	}

	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	rows := make([]queryRow, 0, len(ids))
	for _, id := range ids {
		fields, err := decode(docs[id])
		if err != nil {
			return nil, err
		}
		if !matchesFilters(fields, id, filters) {
			continue
		}
		if !hasOrderFields(fields, id, orders) {
			continue
		}
		if afterVals != nil && !isStrictlyAfter(fields, id, orders, afterVals) {
			continue
		}
		rows = append(rows, queryRow{id: id, fields: fields})
	}

	slices.SortFunc(rows, func(a, b queryRow) int {
		for _, o := range orders {
			va, _ := lookupField(a.fields, a.id, o.Field)
			vb, _ := lookupField(b.fields, b.id, o.Field)
			c := compareJSONB(va, vb)
			if o.Direction == docstore.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return 0
	})

	if s.Count > 0 && len(rows) > s.Count {
		rows = rows[:s.Count]
	}

	result := make([]docstore.Record, 0, len(rows))
	for _, row := range rows {
		fields := row.fields
		if s.Fields != nil {
			selected := map[string]any{}
			for _, name := range s.Fields {
				if v, ok := fields[name]; ok {
					selected[name] = v
				}
			}
			fields = selected
		}
		result = append(result, docstore.Record{
			Path:   s.Collection + "/" + row.id,
			Fields: fields,
		})
	}
	return result, nil
}

func matchesFilters(fields map[string]any, id string, filters []compiledFilter) bool {
	for _, f := range filters {
		val, ok := lookupField(fields, id, f.field)
		if !ok {
			return false
		}
		switch f.op {
		case "==":
			if compareJSONB(val, f.val) != 0 {
				return false
			}
		case ">":
			if compareJSONB(val, f.val) <= 0 {
				return false
			}
		case ">=":
			if compareJSONB(val, f.val) < 0 {
				return false
			}
		case "<":
			if compareJSONB(val, f.val) >= 0 {
				return false
			}
		case "<=":
			if compareJSONB(val, f.val) > 0 {
				return false
			}
		case "in":
			matched := false
			for _, elem := range f.inSet {
				if compareJSONB(val, elem) == 0 {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func hasOrderFields(fields map[string]any, id string, orders []docstore.Order) bool {
	for _, o := range orders {
		if o.Field == docstore.DocumentID {
			continue
		}
		if _, ok := lookupField(fields, id, o.Field); !ok {
			return false
		}
	}
	return true
}

func isStrictlyAfter(fields map[string]any, id string, orders []docstore.Order, afterVals []any) bool {
	for i, o := range orders {
		val, ok := lookupField(fields, id, o.Field)
		if !ok {
			return false
		}
		c := compareJSONB(val, afterVals[i])
		if o.Direction == docstore.Desc {
			c = -c
		}
		if c > 0 {
			return true
		}
		if c < 0 {
			return false
		}
	}
	return false
}
