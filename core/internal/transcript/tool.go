package transcript

// Tool declares a function the model may call. A ToolCall in a transcript names it.
type Tool struct {
	Name        string
	Description string
	// Parameters describes the call's arguments as an object schema. Nil means no arguments.
	Parameters *Schema
}

// SchemaType is the JSON type a Schema describes.
type SchemaType string

const (
	TypeObject  SchemaType = "object"
	TypeString  SchemaType = "string"
	TypeArray   SchemaType = "array"
	TypeBoolean SchemaType = "boolean"
)

// Schema is the subset of JSON Schema that Nori's tools use. Adapters translate it to
// whatever their provider requires.
type Schema struct {
	Type        SchemaType
	Description string
	Enum        []string
	// Properties and Required apply to objects. Items applies to arrays.
	Properties map[string]*Schema
	Required   []string
	Items      *Schema
}
