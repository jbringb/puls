package model

import (
	"encoding/json"
	"time"
)

type DeviceStatus string

const (
	StatusOnline  DeviceStatus = "online"
	StatusOffline DeviceStatus = "offline"
	StatusUnknown DeviceStatus = "unknown"
)

type DeviceOS string

const (
	OSWindows DeviceOS = "windows"
	OSLinux   DeviceOS = "linux"
)

type Device struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	OS           DeviceOS     `json:"os"`
	Arch         string       `json:"arch"`
	Status       DeviceStatus `json:"status"`
	RegisteredAt time.Time    `json:"registeredAt"`
	LastSeenAt   *time.Time   `json:"lastSeenAt,omitempty"`
}

type RegisterRequest struct {
	Name   string   `json:"name"`
	OS     DeviceOS `json:"os"`
	Arch   string   `json:"arch"`
	Secret string   `json:"secret"` // plain-text; hashed before storage
}

type RegisterResponse struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

type Heartbeat struct {
	ID            int64     `json:"id"`
	DeviceID      string    `json:"deviceId"`
	ReceivedAt    time.Time `json:"receivedAt"`
	CPUPercent    float32   `json:"cpuPercent"`
	MemoryPercent float32   `json:"memoryPercent"`
	DiskPercent   float32   `json:"diskPercent"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
	OSVersion     string    `json:"osVersion"`
}

type DiagnosticScope string

const (
	ScopeFull      DiagnosticScope = "full"
	ScopeNetwork   DiagnosticScope = "network"
	ScopeProcesses DiagnosticScope = "processes"
	ScopeStorage   DiagnosticScope = "storage"
)

// DiagnosticRequestStatus is derived, not stored — see server.diagnosticStatus.
// It exists so a caller polling for a result can tell "still waiting on the
// device" (Pending) apart from "delivered but the device never answered, and
// won't now" (TimedOut), which an absent Payload alone can't distinguish.
type DiagnosticRequestStatus string

const (
	DiagnosticPending   DiagnosticRequestStatus = "pending"
	DiagnosticCompleted DiagnosticRequestStatus = "completed"
	DiagnosticTimedOut  DiagnosticRequestStatus = "timed_out"
)

type DiagnosticResult struct {
	ID          int64                   `json:"id"`
	DeviceID    string                  `json:"deviceId"`
	RequestID   string                  `json:"requestId"`
	Scope       DiagnosticScope         `json:"scope"`
	RequestedAt time.Time               `json:"requestedAt"`
	ReceivedAt  *time.Time              `json:"receivedAt,omitempty"`
	Payload     *json.RawMessage        `json:"payload,omitempty"`
	Status      DiagnosticRequestStatus `json:"status"`
}

type DeviceDetail struct {
	Device
	RecentHeartbeats []Heartbeat `json:"recentHeartbeats"`
}

// DeviceList is a page of ListDevices results. A non-empty NextCursor means
// more devices remain; pass it back as the next request's ?cursor= to
// continue.
type DeviceList struct {
	Devices    []Device `json:"devices"`
	NextCursor string   `json:"nextCursor,omitempty"`
}
