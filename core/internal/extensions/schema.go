package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// MaxSchemaBytes bounds a tool's canonical schema. Larger provider schemas are rejected visibly.
const MaxSchemaBytes = 64 << 10

// OpenAITransform is the model-schema projection for OpenAI function tools in non-strict mode.
const OpenAITransform = "openai-functions-1"

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema reference %s is not bundled; external references are not fetched", url)
}

// Schema is a compiled canonical schema.
type Schema struct{ compiled *jsonschema.Schema }

// CompileSchema compiles a canonical JSON Schema 2020-12 document. External references are refused; only references
// inside the document resolve.
func CompileSchema(raw json.RawMessage) (*Schema, error) {
	if len(raw) == 0 {
		return nil, errors.New("input schema is required")
	}
	if len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("schema exceeds %d bytes", MaxSchemaBytes)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("schema is not valid JSON")
	}
	object, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("schema must be a JSON object")
	}
	if kind, present := object["type"]; present && kind != "object" {
		return nil, errors.New("tool input schema must describe an object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(noLoader{})
	const location = "urn:nori:tool-schema"
	if err = compiler.AddResource(location, doc); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("schema does not compile: %v", err)
	}
	return &Schema{compiled: compiled}, nil
}

// Validate checks decoded arguments. Numbers decoded by encoding/json as float64 are accepted.
func (s *Schema) Validate(args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	// Round-trip so integers are json.Number, which the validator treats exactly.
	data, err := json.Marshal(args)
	if err != nil {
		return errors.New("arguments are not valid JSON")
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return errors.New("arguments are not valid JSON")
	}
	if err = s.compiled.Validate(value); err != nil {
		var invalid *jsonschema.ValidationError
		if errors.As(err, &invalid) {
			return fmt.Errorf("arguments do not match the tool schema: %s", firstLine(invalid.Error()))
		}
		return err
	}
	return nil
}

func firstLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 1 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return text
}

// ModelSchema projects a canonical schema for the model provider. It removes document identity keywords the
// provider does not need and refuses constructs it cannot carry. Validation always uses the canonical schema.
func ModelSchema(raw json.RawMessage) (map[string]any, string, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, "", errors.New("schema is not a JSON object")
	}
	changed := false
	for _, key := range []string{"$schema", "$id", "$comment"} {
		if _, ok := schema[key]; ok {
			delete(schema, key)
			changed = true
		}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
		changed = true
	}
	if _, ok := schema["properties"]; !ok {
		schema["properties"] = map[string]any{}
		changed = true
	}
	if err := checkRefs(schema); err != nil {
		return nil, "", err
	}
	if !changed {
		return schema, "", nil
	}
	return schema, OpenAITransform, nil
}

func checkRefs(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if (key == "$ref" || key == "$dynamicRef") && !strings.HasPrefix(fmt.Sprint(child), "#") {
				return fmt.Errorf("schema reference %v is external and cannot be sent to the model", child)
			}
			if err := checkRefs(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := checkRefs(child); err != nil {
				return err
			}
		}
	}
	return nil
}
