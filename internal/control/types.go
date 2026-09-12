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

type DNSPolicyAction string

const (
	DNSPolicyAllow   DNSPolicyAction = "allow"
	DNSPolicyBlock   DNSPolicyAction = "block"
	DNSPolicyRewrite DNSPolicyAction = "rewrite"
)

type DNSPolicyMatchType string

const (
	DNSPolicyMatchExact   DNSPolicyMatchType = "exact"
	DNSPolicyMatchSuffix  DNSPolicyMatchType = "suffix"
	DNSPolicyMatchKeyword DNSPolicyMatchType = "keyword"
	DNSPolicyMatchRegexp  DNSPolicyMatchType = "regexp"
)

type DNSPolicyRewriteType string

const (
	DNSPolicyRewriteA     DNSPolicyRewriteType = "A"
	DNSPolicyRewriteAAAA  DNSPolicyRewriteType = "AAAA"
	DNSPolicyRewriteCNAME DNSPolicyRewriteType = "CNAME"
)

type DNSPolicySettings struct {
	UserID               string     `json:"user_id"`
	StripECS             bool       `json:"strip_ecs"`
	BlockPrivateAnswers  bool       `json:"block_private_answers"`
	BlockedQTypes        []string   `json:"blocked_qtypes"`
	CustomBlockEnabled   bool       `json:"custom_block_enabled"`
	CustomAllowEnabled   bool       `json:"custom_allow_enabled"`
	CustomRewriteEnabled bool       `json:"custom_rewrite_enabled"`
	PolicyPausedUntil    *time.Time `json:"policy_paused_until"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type DNSPolicySettingsPatch struct {
	StripECS             *bool      `json:"strip_ecs,omitempty"`
	BlockPrivateAnswers  *bool      `json:"block_private_answers,omitempty"`
	BlockedQTypes        *[]string  `json:"blocked_qtypes,omitempty"`
	CustomBlockEnabled   *bool      `json:"custom_block_enabled,omitempty"`
	CustomAllowEnabled   *bool      `json:"custom_allow_enabled,omitempty"`
	CustomRewriteEnabled *bool      `json:"custom_rewrite_enabled,omitempty"`
	PolicyPausedUntil    *time.Time `json:"policy_paused_until,omitempty"`
}

type DNSPolicyRule struct {
	ID         string               `json:"id"`
	UserID     string               `json:"user_id"`
	Enabled    bool                 `json:"enabled"`
	Priority   uint32               `json:"priority"`
	Action     DNSPolicyAction      `json:"action"`
	Match      DNSPolicyMatchType   `json:"match"`
	Pattern    string               `json:"pattern"`
	RecordType DNSPolicyRewriteType `json:"record_type,omitempty"`
	Value      string               `json:"value,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

type DNSPolicyRuleSpec struct {
	Enabled    bool                 `json:"enabled"`
	Priority   uint32               `json:"priority"`
	Action     DNSPolicyAction      `json:"action"`
	Match      DNSPolicyMatchType   `json:"match"`
	Pattern    string               `json:"pattern"`
	RecordType DNSPolicyRewriteType `json:"record_type,omitempty"`
	Value      string               `json:"value,omitempty"`
}

type DNSPolicyRulePatch struct {
	Enabled    *bool                 `json:"enabled,omitempty"`
	Priority   *uint32               `json:"priority,omitempty"`
	Action     *DNSPolicyAction      `json:"action,omitempty"`
	Match      *DNSPolicyMatchType   `json:"match,omitempty"`
	Pattern    *string               `json:"pattern,omitempty"`
	RecordType *DNSPolicyRewriteType `json:"record_type,omitempty"`
	Value      *string               `json:"value,omitempty"`
}

type PublicListFormat string

type PublicListRefreshStatus string

const (
	PublicListFormatMosDNS PublicListFormat = "mosdns"
	PublicListFormatHosts  PublicListFormat = "hosts"

	PublicListRefreshNever   PublicListRefreshStatus = "never"
	PublicListRefreshSuccess PublicListRefreshStatus = "success"
	PublicListRefreshError   PublicListRefreshStatus = "error"
)

type PublicList struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name"`
	Category          string                  `json:"category"`
	URL               string                  `json:"url"`
	Format            PublicListFormat        `json:"format"`
	Enabled           bool                    `json:"enabled"`
	SHA256            string                  `json:"sha256,omitempty"`
	RefreshSeconds    uint32                  `json:"refresh_seconds"`
	EntryCount        uint64                  `json:"entry_count"`
	LastRefreshStatus PublicListRefreshStatus `json:"last_refresh_status"`
	LastRefreshedAt   *time.Time              `json:"last_refreshed_at"`
	LastRefreshError  string                  `json:"last_refresh_error,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
}

type PublicListSpec struct {
	Name           string           `json:"name"`
	Category       string           `json:"category"`
	URL            string           `json:"url"`
	Format         PublicListFormat `json:"format"`
	Enabled        bool             `json:"enabled"`
	SHA256         string           `json:"sha256,omitempty"`
	RefreshSeconds uint32           `json:"refresh_seconds"`
}

type PublicListPatch struct {
	Name           *string           `json:"name,omitempty"`
	Category       *string           `json:"category,omitempty"`
	URL            *string           `json:"url,omitempty"`
	Format         *PublicListFormat `json:"format,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	SHA256         *string           `json:"sha256,omitempty"`
	RefreshSeconds *uint32           `json:"refresh_seconds,omitempty"`
}

type PublicListRefreshResult struct {
	Status      PublicListRefreshStatus
	EntryCount  uint64
	RefreshedAt time.Time
	Error       string
}

type UserPublicList struct {
	List       PublicList `json:"list"`
	Enabled    bool       `json:"enabled"`
	Overridden bool       `json:"overridden"`
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
	GetDNSPolicySettings(ctx context.Context, userID string) (DNSPolicySettings, error)
	UpdateDNSPolicySettings(ctx context.Context, actorID, userID string, patch DNSPolicySettingsPatch) (DNSPolicySettings, error)
	CreateDNSPolicyRule(ctx context.Context, actorID, userID string, spec DNSPolicyRuleSpec) (DNSPolicyRule, error)
	GetDNSPolicyRule(ctx context.Context, userID, ruleID string) (DNSPolicyRule, error)
	UpdateDNSPolicyRule(ctx context.Context, actorID, userID, ruleID string, patch DNSPolicyRulePatch) (DNSPolicyRule, error)
	DeleteDNSPolicyRule(ctx context.Context, actorID, userID, ruleID string) error
	ListDNSPolicyRules(ctx context.Context, userID string, page Page) (PageResult[DNSPolicyRule], error)
	CreatePublicList(ctx context.Context, actorID string, spec PublicListSpec) (PublicList, error)
	UpdatePublicList(ctx context.Context, actorID, listID string, patch PublicListPatch) (PublicList, error)
	DeletePublicList(ctx context.Context, actorID, listID string) error
	GetPublicList(ctx context.Context, listID string) (PublicList, error)
	ListPublicLists(ctx context.Context, page Page) (PageResult[PublicList], error)
	RecordPublicListRefresh(ctx context.Context, listID string, result PublicListRefreshResult) error
	SetUserPublicList(ctx context.Context, actorID, userID, listID string, enabled *bool) error
	ListUserPublicLists(ctx context.Context, userID string, page Page) (PageResult[UserPublicList], error)
	ListAudit(ctx context.Context, from, to time.Time, page Page) (PageResult[AuditRecord], error)
	Close() error
}

type Maintainer interface {
	Maintain(context.Context) error
	RunMaintenance(context.Context, time.Duration) error
}
