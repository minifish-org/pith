package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// JSON-RPC 2.0 error codes.
const (
	JSONRPCCodeParseError     = -32700
	JSONRPCCodeInvalidRequest = -32600
	JSONRPCCodeMethodNotFound = -32601
	JSONRPCCodeInvalidParams  = -32602
	JSONRPCCodeInternalError  = -32603
)

// JsonRpcId is a JSON-RPC request identifier: a string or a finite number.
// The two forms are kept distinct so ids round-trip exactly and can be used as
// map keys for request correlation.
type JsonRpcId struct {
	str   string
	num   float64
	isNum bool
}

// StringID builds a string JSON-RPC id.
func StringID(value string) JsonRpcId { return JsonRpcId{str: value} }

// NumberID builds a numeric JSON-RPC id.
func NumberID(value float64) JsonRpcId { return JsonRpcId{num: value, isNum: true} }

// IsNumber reports whether the id is numeric.
func (id JsonRpcId) IsNumber() bool { return id.isNum }

// Number returns the numeric value, or 0 for a string id.
func (id JsonRpcId) Number() float64 { return id.num }

// StringValue renders the id the way a server would see it.
func (id JsonRpcId) StringValue() string {
	if id.isNum {
		return strconv.FormatFloat(id.num, 'f', -1, 64)
	}
	return id.str
}

// String implements fmt.Stringer.
func (id JsonRpcId) String() string { return id.StringValue() }

// MarshalJSON writes the id in its original JSON form.
func (id JsonRpcId) MarshalJSON() ([]byte, error) {
	if id.isNum {
		if math.IsInf(id.num, 0) || math.IsNaN(id.num) {
			return nil, errors.New("mcp: non-finite JSON-RPC id")
		}
		return []byte(strconv.FormatFloat(id.num, 'f', -1, 64)), nil
	}
	return json.Marshal(id.str)
}

// UnmarshalJSON reads a string or number id.
func (id *JsonRpcId) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return errors.New("mcp: empty JSON-RPC id")
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &id.str)
	}
	var number float64
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return fmt.Errorf("mcp: invalid JSON-RPC id: %w", err)
	}
	if math.IsInf(number, 0) || math.IsNaN(number) {
		return errors.New("mcp: non-finite JSON-RPC id")
	}
	id.num = number
	id.isNum = true
	return nil
}

// ErrorObject is the `error` member of a JSON-RPC error response.
type ErrorObject struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// McpError is a JSON-RPC or protocol error carrying a code and optional data.
type McpError struct {
	Code    int
	Message string
	Data    any
}

// NewMcpError builds an McpError. The optional data argument carries the
// JSON-RPC error data.
func NewMcpError(code int, message string, data ...any) *McpError {
	err := &McpError{Code: code, Message: message}
	if len(data) > 0 {
		err.Data = data[0]
	}
	return err
}

// Error implements the error interface.
func (e *McpError) Error() string { return e.Message }

// McpConnectionClosedError reports use of a closed or never-open connection.
type McpConnectionClosedError struct {
	Message string
}

// NewConnectionClosedError builds a connection-closed error.
func NewConnectionClosedError(message string) *McpConnectionClosedError {
	if message == "" {
		message = "MCP connection closed"
	}
	return &McpConnectionClosedError{Message: message}
}

// Error implements the error interface.
func (e *McpConnectionClosedError) Error() string { return e.Message }

// McpTimeoutError reports a request that outlived its timeout.
type McpTimeoutError struct {
	Timeout time.Duration
}

// Error implements the error interface.
func (e *McpTimeoutError) Error() string {
	return fmt.Sprintf("MCP request timed out after %dms", e.Timeout.Milliseconds())
}

// McpAbortError reports a request cancelled by its caller.
type McpAbortError struct {
	Message string
}

// NewAbortError builds an abort error.
func NewAbortError(message string) *McpAbortError {
	if message == "" {
		message = "MCP request aborted"
	}
	return &McpAbortError{Message: message}
}

// Error implements the error interface.
func (e *McpAbortError) Error() string { return e.Message }

// Is lets errors.Is treat a wrapped abort like any abort.
func (e *McpAbortError) Is(target error) bool {
	_, ok := target.(*McpAbortError)
	return ok
}

// Message is a JSON-RPC 2.0 request, notification or response.
//
// Exactly one of the three shapes is represented:
//   - request: ID != nil and Method != ""
//   - notification: ID == nil and Method != ""
//   - response: ID != nil, Method == "" and (Result != nil or Error != nil)
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *JsonRpcId       `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *ErrorObject     `json:"error,omitempty"`
}

// NewRequestMessage builds a JSON-RPC request.
func NewRequestMessage(id JsonRpcId, method string, params json.RawMessage) *Message {
	return &Message{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
}

// NewNotificationMessage builds a JSON-RPC notification.
func NewNotificationMessage(method string, params json.RawMessage) *Message {
	return &Message{JSONRPC: "2.0", Method: method, Params: params}
}

// NewResultMessage builds a successful JSON-RPC response.
func NewResultMessage(id JsonRpcId, result json.RawMessage) *Message {
	if result == nil {
		result = json.RawMessage("null")
	}
	value := result
	return &Message{JSONRPC: "2.0", ID: &id, Result: &value}
}

// NewErrorMessage builds a JSON-RPC error response.
func NewErrorMessage(id JsonRpcId, err *ErrorObject) *Message {
	return &Message{JSONRPC: "2.0", ID: &id, Error: err}
}

// IsRequest reports whether the message is a request expecting a response.
func (m *Message) IsRequest() bool { return m != nil && m.ID != nil && m.Method != "" }

// IsNotification reports whether the message is a one-way notification.
func (m *Message) IsNotification() bool { return m != nil && m.ID == nil && m.Method != "" }

// IsResponse reports whether the message is a response to a request.
func (m *Message) IsResponse() bool {
	return m != nil && m.ID != nil && m.Method == "" && (m.Result != nil || m.Error != nil)
}

// IsObject reports whether a decoded JSON value is a JSON object.
func IsObject(value any) bool {
	if value == nil {
		return false
	}
	_, ok := value.(map[string]any)
	return ok
}

// ParseMessage decodes and validates a single JSON-RPC message.
func ParseMessage(data []byte) (*Message, error) {
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, NewMcpError(JSONRPCCodeParseError, "Invalid JSON-RPC message")
	}
	if message.JSONRPC != "2.0" {
		return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
	}
	if message.Method != "" {
		if message.Result != nil || message.Error != nil {
			return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
		}
		return &message, nil
	}
	if message.ID == nil {
		return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
	}
	if message.Result != nil && message.Error != nil {
		return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
	}
	if message.Result == nil && message.Error == nil {
		return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
	}
	if message.Error != nil && message.Error.Message == "" {
		return nil, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid JSON-RPC message")
	}
	return &message, nil
}

// decodeParams decodes message params into a generic JSON value.
func (m *Message) decodeParams() (any, error) {
	if len(m.Params) == 0 || string(m.Params) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(m.Params, &value); err != nil {
		return nil, err
	}
	return value, nil
}
