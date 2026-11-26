package transcript

type Tool struct {
	Name        string
	Description string
	Parameters  *Schema
	Schema      map[string]any
}

type SchemaType string

const (
	TypeObject  SchemaType = "object"
	TypeString  SchemaType = "string"
	TypeArray   SchemaType = "array"
	TypeBoolean SchemaType = "boolean"
)

type Schema struct {
	Type        SchemaType
	Description string
	Enum        []string
	Properties  map[string]*Schema
	Required    []string
	Items       *Schema
}
