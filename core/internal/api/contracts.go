package api

import (
	"bytes"
	"encoding/json"
	"fmt"

	"nori/internal/computersession"
	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/plugins"
	"nori/internal/scheduler"
	"nori/internal/state"
)

// ContractTypes maps a contract schema reference to a constructor for the Go type the server reads or writes for it.
// contracts-check decodes every valid fixture into its type with unknown fields disallowed, re-encodes it and
// validates the result again, so a field the schema allows but the server drops, or a field the server writes but the
// schema forbids, fails the check. References missing here are served through ad hoc request structs inside handlers.
var ContractTypes = map[string]func() any{
	"automations.schema.json#/$defs/Automation":                func() any { return &scheduler.Automation{} },
	"automations.schema.json#/$defs/AutomationChange":          func() any { return &envelope[scheduler.Editable]{} },
	"automations.schema.json#/$defs/AutomationPreview":         func() any { return &scheduler.Preview{} },
	"automations.schema.json#/$defs/AutomationAction":          func() any { return &envelope[scheduler.Action]{} },
	"automations.schema.json#/$defs/Occurrence":                func() any { return &scheduler.Occurrence{} },
	"automations.schema.json#/$defs/Delivery":                  func() any { return &scheduler.Delivery{} },
	"automations.schema.json#/$defs/DeliveryDestination":       func() any { return &scheduler.Destination{} },
	"automations.schema.json#/$defs/DeliveryDestinationCreate": func() any { return &scheduler.DestinationEditable{} },
	"automations.schema.json#/$defs/DeliveryDestinationAction": func() any { return &scheduler.DestinationAction{} },
	"automations.schema.json#/$defs/Goal":                      func() any { return &scheduler.Goal{} },
	"automations.schema.json#/$defs/GoalAction":                func() any { return &envelope[scheduler.GoalAction]{} },
	"automations.schema.json#/$defs/GoalChange":                func() any { return &envelope[scheduler.GoalEditable]{} },
	"library.schema.json#/$defs/InboxItem":                     func() any { return &scheduler.InboxItem{} },
	"library.schema.json#/$defs/InboxAction":                   func() any { return &envelope[inboxAction]{} },
	"library.schema.json#/$defs/LibraryItem":                   func() any { return &libraryItem{} },
	"library.schema.json#/$defs/LibraryItemChange":             func() any { return &envelope[libraryItemEditable]{} },
	"library.schema.json#/$defs/LibraryAction":                 func() any { return &envelope[libraryActionValue]{} },
	"library.schema.json#/$defs/SearchHit":                     func() any { return &searchHit{} },
	"library.schema.json#/$defs/SearchResponse":                func() any { return &searchResponse{} },
	"library.schema.json#/$defs/MemoryFact":                    func() any { return &memoryFact{} },
	"library.schema.json#/$defs/MemoryFactChange":              func() any { return &envelope[memoryFactEditable]{} },
	"library.schema.json#/$defs/MemoryAction":                  func() any { return &envelope[memoryActionValue]{} },
	"library.schema.json#/$defs/UploadAccept":                  func() any { return &uploadAcceptView{} },
	"common.schema.json#/$defs/APIError":                       func() any { return &apiError{} },
	"common.schema.json#/$defs/MutationReceipt":                func() any { return &state.MutationReceipt{} },
	"common.schema.json#/$defs/AssistantSummary":               func() any { return &summary{} },
	"common.schema.json#/$defs/Preferences":                    func() any { return &connections.Preferences{} },
	"common.schema.json#/$defs/PreferencesChange":              func() any { return &envelope[connections.PreferencesEditable]{} },
	"connections.schema.json#/$defs/Connection":                func() any { return &connections.Projection{} },
	"connections.schema.json#/$defs/ConnectionAction":          func() any { return &envelope[connections.Action]{} },
	"connections.schema.json#/$defs/ConnectionChange":          func() any { return &envelope[connections.Editable]{} },
	"connections.schema.json#/$defs/Integration":               func() any { return &connections.IntegrationEntry{} },
	"connections.schema.json#/$defs/Resource":                  func() any { return &connections.Resource{} },
	"execution.schema.json#/$defs/WorkItem":                    func() any { return &workItem{} },
	"execution.schema.json#/$defs/WorkDetail":                  func() any { return &workDetail{} },
	"execution.schema.json#/$defs/OperationProjection":         func() any { return &operationProjection{} },
	"permissions.schema.json#/$defs/Interaction":               func() any { return &permissions.Interaction{} },
	"permissions.schema.json#/$defs/InteractionResponse":       func() any { return &envelope[permissions.Response]{} },
	"permissions.schema.json#/$defs/PermissionRule":            func() any { return &permissions.Rule{} },
	"permissions.schema.json#/$defs/PermissionRuleChange":      func() any { return &envelope[permissions.RuleEditable]{} },
	"plugins.schema.json#/$defs/CatalogEntry":                  func() any { return &plugins.CatalogEntry{} },
	"plugins.schema.json#/$defs/Installation":                  func() any { return &plugins.Installation{} },
	"plugins.schema.json#/$defs/InstallationCreateChange":      func() any { return &envelope[plugins.InstallationCreate]{} },
	"plugins.schema.json#/$defs/InstallationAction":            func() any { return &envelope[plugins.InstallationAction]{} },
	"plugins.schema.json#/$defs/InstallationReview":            func() any { return &plugins.Review{} },
	"plugins.schema.json#/$defs/PackageManifest":               func() any { return &plugins.Manifest{} },
	"plugins.schema.json#/$defs/Skill":                         func() any { return &plugins.Skill{} },
	"plugins.schema.json#/$defs/SkillView":                     func() any { return &plugins.SkillView{} },
	"plugins.schema.json#/$defs/SkillChange":                   func() any { return &envelope[plugins.SkillEditable]{} },
	"plugins.schema.json#/$defs/MCPServer":                     func() any { return &mcp.Server{} },
	"plugins.schema.json#/$defs/MCPServerChange":               func() any { return &envelope[mcp.Editable]{} },
	"plugins.schema.json#/$defs/MCPServerAction":               func() any { return &envelope[mcp.Action]{} },
	"plugins.schema.json#/$defs/MCPCredentialInput":            func() any { return &mcp.CredentialInput{} },
	"plugins.schema.json#/$defs/MCPServerTool":                 func() any { return &mcp.Tool{} },
	"plugins.schema.json#/$defs/MCPClient":                     func() any { return &mcpClient{} },
	"plugins.schema.json#/$defs/MCPClientChange":               func() any { return &envelope[mcpClientEditable]{} },
	"plugins.schema.json#/$defs/MCPClientCreated":              func() any { return &mcpClientCreated{} },
	"plugins.schema.json#/$defs/MCPClientAction":               func() any { return &envelope[mcpClientActionValue]{} },
	"plugins.schema.json#/$defs/ComputerSession":               func() any { return &computersession.Session{} },
	"plugins.schema.json#/$defs/ComputerSessionAction":         func() any { return &envelope[computersession.Action]{} },
	"tools.schema.json#/$defs/ToolDefinition":                  func() any { return &extensions.Definition{} },
	"tools.schema.json#/$defs/AvailableTool":                   func() any { return &availableTool{} },
}

// envelope is the Change / ActionRequest wrapper requests carry around their value.
type envelope[T any] struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	Value            T      `json:"value"`
}

// RoundTrip decodes a fixture into the registered type with unknown fields disallowed and returns its re-encoding.
func RoundTrip(ref string, data []byte) ([]byte, error) {
	construct, ok := ContractTypes[ref]
	if !ok {
		return nil, fmt.Errorf("no Go type registered for %s", ref)
	}
	value := construct()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
