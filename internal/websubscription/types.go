package websubscription

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentText  ContentType = "text"
	ContentImage ContentType = "image"
	ContentFile  ContentType = "file"
)

type Content struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	URL      string      `json:"url,omitempty"`
	FileID   string      `json:"file_id,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
}

type Message struct {
	Role       Role      `json:"role"`
	Content    []Content `json:"content,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
	ToolOutput string    `json:"tool_output,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
}

type ToolType string

const (
	ToolFunction ToolType = "function"
	ToolCustom   ToolType = "custom"
)

type ToolDefinition struct {
	Type        ToolType        `json:"type"`
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolChoiceMode string

const (
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

type ToolChoice struct {
	Mode      ToolChoiceMode `json:"mode"`
	Type      ToolType       `json:"type,omitempty"`
	Name      string         `json:"name,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
}

type Turn struct {
	RequestID         string           `json:"request_id"`
	Model             string           `json:"model"`
	Messages          []Message        `json:"messages"`
	Tools             []ToolDefinition `json:"tools,omitempty"`
	ToolChoice        ToolChoice       `json:"tool_choice"`
	ParallelToolCalls bool             `json:"parallel_tool_calls,omitempty"`
}

type ToolAction struct {
	CallID    string          `json:"call_id"`
	Type      ToolType        `json:"type"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"`
}
