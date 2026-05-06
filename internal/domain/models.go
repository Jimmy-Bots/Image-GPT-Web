package domain

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
	UserStatusDeleted  UserStatus = "deleted"
)

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"-"`
	Role         Role       `json:"role"`
	Status       UserStatus `json:"status"`
	APIKey       *APIKey    `json:"api_key,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	KeyHash    string     `json:"-"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type Account struct {
	AccessToken       string          `json:"access_token"`
	Password          string          `json:"password,omitempty"`
	Type              string          `json:"type"`
	Status            string          `json:"status"`
	Quota             int             `json:"quota"`
	MaxConcurrency    int             `json:"max_concurrency"`
	ImageQuotaUnknown bool            `json:"image_quota_unknown"`
	Email             string          `json:"email,omitempty"`
	UserID            string          `json:"user_id,omitempty"`
	LimitsProgress    json.RawMessage `json:"limits_progress,omitempty"`
	DefaultModelSlug  string          `json:"default_model_slug,omitempty"`
	RestoreAt         string          `json:"restore_at,omitempty"`
	Success           int             `json:"success"`
	Fail              int             `json:"fail"`
	ActiveRequests    int             `json:"active_requests,omitempty"`
	AllowedConcurrency int            `json:"allowed_concurrency,omitempty"`
	LastUsedAt        string          `json:"last_used_at,omitempty"`
	RawJSON           json.RawMessage `json:"-"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type SystemLog struct {
	ID      string          `json:"id"`
	Time    time.Time       `json:"time"`
	Type    string          `json:"type"`
	Summary string          `json:"summary"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

type ImageTask struct {
	ID        string          `json:"id"`
	OwnerID   string          `json:"-"`
	Status    string          `json:"status"`
	Mode      string          `json:"mode"`
	Model     string          `json:"model,omitempty"`
	Size      string          `json:"size,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
