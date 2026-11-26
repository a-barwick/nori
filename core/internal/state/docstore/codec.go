package docstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeFor[time.Time]()

func Encode(v any) (map[string]any, error) {
	x, e := Value(v)
	if e != nil {
		return nil, e
	}
	m, ok := x.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("document must be an object")
	}
	return m, nil
}
func Value(v any) (any, error) { return encode(reflect.ValueOf(v)) }
func encode(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		return encode(v.Elem())
	}
	if v.Type() == timeType {
		return v.Interface(), nil
	}
	switch v.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("firestore"), ",")
			if tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			if len(tag) > 1 && tag[1] == "omitempty" && empty(v.Field(i)) {
				continue
			}
			value, e := encode(v.Field(i))
			if e != nil {
				return nil, e
			}
			if f.Anonymous && tag[0] == "" {
				if embedded, ok := value.(map[string]any); ok {
					for k, x := range embedded {
						out[k] = x
					}
				}
				continue
			}
			out[name] = value
		}
		return out, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			if iter.Key().Kind() != reflect.String {
				return nil, fmt.Errorf("document map key must be text")
			}
			x, e := encode(iter.Value())
			if e != nil {
				return nil, e
			}
			out[iter.Key().String()] = x
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return append([]byte{}, v.Bytes()...), nil
		}
		out := make([]any, v.Len())
		for i := range out {
			x, e := encode(v.Index(i))
			if e != nil {
				return nil, e
			}
			out[i] = x
		}
		return out, nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() > 1<<63-1 {
			return nil, fmt.Errorf("document integer overflow")
		}
		return int64(v.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	default:
		return nil, fmt.Errorf("unsupported document value %s", v.Kind())
	}
}
func empty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}
func Decode(fields map[string]any, target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("document target must be a pointer")
	}
	return decode(fields, v.Elem())
}
func decode(x any, v reflect.Value) error {
	if x == nil {
		v.SetZero()
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return decode(x, v.Elem())
	}
	if v.Type() == timeType {
		switch value := x.(type) {
		case time.Time:
			v.Set(reflect.ValueOf(value))
			return nil
		case string:
			t, e := time.Parse(time.RFC3339Nano, value)
			if e != nil {
				return e
			}
			v.Set(reflect.ValueOf(t))
			return nil
		}
	}
	switch v.Kind() {
	case reflect.Interface:
		v.Set(reflect.ValueOf(x))
		return nil
	case reflect.Struct:
		m, ok := x.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object for %s", v.Type())
		}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("firestore"), ",")[0]
			if tag == "-" {
				continue
			}
			if f.Anonymous && tag == "" {
				if e := decode(m, v.Field(i)); e != nil {
					return e
				}
				continue
			}
			if tag == "" {
				tag = f.Name
			}
			if value, found := m[tag]; found {
				if e := decode(value, v.Field(i)); e != nil {
					return fmt.Errorf("field %s: %w", tag, e)
				}
			}
		}
		return nil
	case reflect.Map:
		m, ok := x.(map[string]any)
		if !ok {
			return fmt.Errorf("expected map")
		}
		v.Set(reflect.MakeMapWithSize(v.Type(), len(m)))
		for k, value := range m {
			item := reflect.New(v.Type().Elem()).Elem()
			if e := decode(value, item); e != nil {
				return e
			}
			v.SetMapIndex(reflect.ValueOf(k).Convert(v.Type().Key()), item)
		}
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			switch b := x.(type) {
			case []byte:
				v.SetBytes(b)
				return nil
			case string:
				raw, e := base64.StdEncoding.DecodeString(b)
				if e != nil {
					return e
				}
				v.SetBytes(raw)
				return nil
			}
		}
		values, ok := x.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		v.Set(reflect.MakeSlice(v.Type(), len(values), len(values)))
		for i, value := range values {
			if e := decode(value, v.Index(i)); e != nil {
				return e
			}
		}
		return nil
	case reflect.String:
		if s, ok := x.(string); ok {
			v.SetString(s)
			return nil
		}
	case reflect.Bool:
		if b, ok := x.(bool); ok {
			v.SetBool(b)
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var n int64
		switch value := x.(type) {
		case int64:
			n = value
		case json.Number:
			var e error
			n, e = value.Int64()
			if e != nil {
				return e
			}
		default:
			return fmt.Errorf("expected integer")
		}
		if v.OverflowInt(n) {
			return fmt.Errorf("integer overflow")
		}
		v.SetInt(n)
		return nil
	case reflect.Float32, reflect.Float64:
		var n float64
		switch value := x.(type) {
		case float64:
			n = value
		case int64:
			n = float64(value)
		case json.Number:
			var e error
			n, e = value.Float64()
			if e != nil {
				return e
			}
		default:
			return fmt.Errorf("expected number")
		}
		v.SetFloat(n)
		return nil
	}
	return fmt.Errorf("cannot decode %T into %s", x, v.Type())
}

func JSONValue(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.UTC().Format("2006-01-02T15:04:05.000000000Z")
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = JSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = JSONValue(v)
		}
		return out
	default:
		return v
	}
}

func NormalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, e := x.Int64(); e == nil {
			return n
		}
		n, _ := x.Float64()
		return n
	case map[string]any:
		for k, v := range x {
			x[k] = NormalizeNumbers(v)
		}
	case []any:
		for i, v := range x {
			x[i] = NormalizeNumbers(v)
		}
	}
	return v
}
