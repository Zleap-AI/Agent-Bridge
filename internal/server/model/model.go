package model

import "time"

type OwnerCredential struct {
	PasswordHash string
	AuthVersion  int64
	UpdatedAt    time.Time
}

type OwnerSession struct {
	ID          string
	TokenHash   string
	ExpiresAt   time.Time
	AuthVersion int64
	CreatedAt   time.Time
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type Device struct {
	BridgeID   string     `json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type Agent struct {
	BridgeID    string    `json:"-"`
	AgentID     string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PairingCode struct {
	ID         string
	CodeHash   string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

type CallRecord struct {
	ID         int64     `json:"id"`
	DeviceID   string    `json:"device_id"`
	AgentID    string    `json:"agent_id"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}
