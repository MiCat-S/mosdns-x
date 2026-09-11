package control

import (
	"context"
	"time"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type Period string

const (
	PeriodDaily   Period = "daily"
	PeriodMonthly Period = "monthly"
)

type User struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	Role           Role      `json:"role"`
	Enabled        bool      `json:"enabled"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Period         Period    `json:"period"`
	Timezone       string    `json:"timezone"`
	Limit          uint64    `json:"limit"`
	QPS            uint32    `json:"qps"`
	Burst          uint32    `json:"burst"`
	MaxCredentials uint32    `json:"max_credentials"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type UserSpec struct {
	Username       string    `json:"username"`
	Password       string    `json:"password"`
	Role           Role      `json:"role"`
	Enabled        bool      `json:"enabled"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Period         Period    `json:"period"`
	Timezone       string    `json:"timezone"`
	Limit          uint64    `json:"limit"`
	QPS            uint32    `json:"qps"`
	Burst          uint32    `json:"burst"`
	MaxCredentials uint32    `json:"max_credentials"`
}

type UserPatch struct {
	Enabled        *bool      `json:"enabled,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Period         *Period    `json:"period,omitempty"`
	Timezone       *string    `json:"timezone,omitempty"`
	Limit          *uint64    `json:"limit,omitempty"`
	QPS            *uint32    `json:"qps,omitempty"`
	Burst          *uint32    `json:"burst,omitempty"`
	MaxCredentials *uint32    `json:"max_credentials,omitempty"`
}

type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type Credential struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	RevokedAt time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type IssuedCredential struct {
	Credential Credential `json:"credential"`
	Token      string     `json:"token"`
}

type Identity struct {
	UserID            string `json:"user_id"`
	CredentialID      string `json:"credential_id"`
	CredentialVersion uint64 `json:"credential_version"`
}

type QuotaStatus struct {
	Period      Period    `json:"period"`
	Timezone    string    `json:"timezone"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Limit       uint64    `json:"limit"`
	Used        uint64    `json:"used"`
	Remaining   uint64    `json:"remaining"`
}

type UsagePoint struct {
	Minute       time.Time `json:"minute"`
	UserID       string    `json:"user_id"`
	CredentialID string    `json:"credential_id,omitempty"`
	Count        uint64    `json:"count"`
}

type AuditRecord struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type Page struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

type PageResult[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type Service interface {
	InitializeAdmin(ctx context.Context, spec UserSpec) (User, error)
	CreateUser(ctx context.Context, actorID string, spec UserSpec) (User, error)
	UpdateUser(ctx context.Context, actorID, userID string, patch UserPatch) (User, error)
	SetPassword(ctx context.Context, actorID, userID, password string) error
	ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error
	AuthenticatePassword(ctx context.Context, username, password string) (User, error)
	Login(ctx context.Context, username, password string, ttl time.Duration) (Session, string, error)
	CreateSession(ctx context.Context, userID string, ttl time.Duration) (Session, string, error)
	AuthenticateSession(ctx context.Context, token string) (Session, User, error)
	RevokeSession(ctx context.Context, actorID, sessionID string) error
	CreateCredential(ctx context.Context, actorID, userID, name string, expiresAt time.Time) (IssuedCredential, error)
	RotateCredential(ctx context.Context, actorID, userID, credentialID string) (IssuedCredential, error)
	RevokeCredential(ctx context.Context, actorID, userID, credentialID string) error
	AuthenticateCredential(ctx context.Context, token string) (Identity, error)
	Admit(ctx context.Context, identity Identity) error
	GetUser(ctx context.Context, userID string) (User, error)
	ListUsers(ctx context.Context, page Page) (PageResult[User], error)
	ListCredentials(ctx context.Context, userID string, page Page) (PageResult[Credential], error)
	CurrentQuota(ctx context.Context, userID string) (QuotaStatus, error)
	Usage(ctx context.Context, userID string, from, to time.Time, page Page) (PageResult[UsagePoint], error)
	CredentialUsage(ctx context.Context, userID, credentialID string, from, to time.Time, page Page) (PageResult[UsagePoint], error)
	ListAudit(ctx context.Context, from, to time.Time, page Page) (PageResult[AuditRecord], error)
	Close() error
}
