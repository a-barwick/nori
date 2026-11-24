package conversation

import (
	"reflect"
	"testing"
)

func TestNullableRoutingEnum(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  []string
		gateway bool
		want    map[string]any
	}{
		{"direct", []string{"task-1"}, false, map[string]any{
			"type": []any{"string", "null"}, "enum": []any{nil, "task-1"},
		}},
		{"gateway with no alternatives", nil, true, map[string]any{"type": "null"}},
		{"gateway with alternatives", []string{"task-1"}, true, map[string]any{
			"anyOf": []any{map[string]any{"type": "string", "enum": []string{"task-1"}}, map[string]any{"type": "null"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := map[string]any{"type": []any{"string", "null"}}
			setNullableStringEnum(field, test.values, test.gateway)
			if !reflect.DeepEqual(field, test.want) {
				t.Fatalf("got %#v, want %#v", field, test.want)
			}
		})
	}
}
