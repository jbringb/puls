package ws

import "encoding/json"

type MessageType string

const (
	TypeHeartbeat    MessageType = "heartbeat"
	TypeDiagRequest  MessageType = "diag_request"
	TypeDiagResponse MessageType = "diag_response"
	TypeError        MessageType = "error"
)

type Envelope struct {
	Type      MessageType     `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type HeartbeatData struct {
	CPUPercent    float32 `json:"cpuPercent"`
	MemoryPercent float32 `json:"memoryPercent"`
	DiskPercent   float32 `json:"diskPercent"`
	UptimeSeconds int64   `json:"uptimeSeconds"`
	OSVersion     string  `json:"osVersion"`
}

type DiagRequestData struct {
	Scope string `json:"scope"`
}

type ErrorData struct {
	Message string `json:"message"`
}

func Encode(msgType MessageType, requestID string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		Type:      msgType,
		RequestID: requestID,
		Data:      raw,
	})
}

func EncodeError(requestID, message string) ([]byte, error) {
	return Encode(TypeError, requestID, ErrorData{Message: message})
}
