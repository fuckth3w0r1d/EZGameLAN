// Package admin defines the small versioned protocol used over SSH.
package admin

import (
	"time"

	"github.com/r3t2/ezgamelan/internal/store"
)

const (
	ProtocolVersion = 1
	MaxRequestSize  = 16 << 10
	MaxResponseSize = 256 << 10
)

type Action string

const (
	ActionSnapshot       Action = "snapshot"
	ActionInvite         Action = "invite"
	ActionRevoke         Action = "revoke"
	ActionDisconnect     Action = "disconnect"
	ActionRestartOpenVPN Action = "restart_openvpn"
	ActionRestartEnroll  Action = "restart_enroll"
	ActionDiagnostics    Action = "diagnostics"
)

var Capabilities = []string{
	string(ActionSnapshot),
	string(ActionInvite),
	string(ActionRevoke),
	string(ActionDisconnect),
	string(ActionRestartOpenVPN),
	string(ActionRestartEnroll),
	string(ActionDiagnostics),
}

type Request struct {
	ProtocolVersion int    `json:"protocol_version"`
	Action          Action `json:"action"`
	Name            string `json:"name,omitempty"`
}

type Response struct {
	ProtocolVersion int          `json:"protocol_version"`
	OK              bool         `json:"ok"`
	Capabilities    []string     `json:"capabilities,omitempty"`
	Snapshot        *Snapshot    `json:"snapshot,omitempty"`
	Invite          *Invite      `json:"invite,omitempty"`
	Diagnostics     string       `json:"diagnostics,omitempty"`
	Message         string       `json:"message,omitempty"`
	Warning         string       `json:"warning,omitempty"`
	Error           *ErrorDetail `json:"error,omitempty"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Snapshot struct {
	GeneratedAt  time.Time `json:"generated_at"`
	PublicHost   string    `json:"public_host"`
	Endpoint     string    `json:"endpoint"`
	EnrollURL    string    `json:"enroll_url"`
	VirtualCIDR  string    `json:"virtual_cidr"`
	OpenVPNState string    `json:"openvpn_state"`
	EnrollState  string    `json:"enroll_state"`
	Devices      []Device  `json:"devices"`
	Warning      string    `json:"warning,omitempty"`
}

type Device struct {
	Name           string             `json:"name"`
	Status         store.ClientStatus `json:"status"`
	Online         bool               `json:"online"`
	VirtualAddress string             `json:"virtual_address,omitempty"`
	RealAddress    string             `json:"real_address,omitempty"`
	BytesReceived  uint64             `json:"bytes_received,omitempty"`
	BytesSent      uint64             `json:"bytes_sent,omitempty"`
	ConnectedSince *time.Time         `json:"connected_since,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	NotAfter       time.Time          `json:"not_after"`
}

type Invite struct {
	Name      string    `json:"name"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}

func success() Response {
	return Response{
		ProtocolVersion: ProtocolVersion,
		OK:              true,
		Capabilities:    append([]string(nil), Capabilities...),
	}
}

func failure(code, message string) Response {
	return Response{
		ProtocolVersion: ProtocolVersion,
		OK:              false,
		Error: &ErrorDetail{
			Code:    code,
			Message: message,
		},
	}
}
