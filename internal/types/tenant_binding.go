package types

import (
	"time"

	"gorm.io/gorm"
)

// Tenant binding statuses.
const (
	TenantBindingStatusActive   = "active"
	TenantBindingStatusDisabled = "disabled"
)

// DefaultTenantBindingSource is the external_source value written by the
// managed-tenant API when the caller does not specify one. A single SaaS
// platform is the only intended writer today; the column exists so future
// sources can coexist without a migration.
const DefaultTenantBindingSource = "qh-saas"

// TenantBinding maps one external (SaaS) tenant reference to a WeKnora
// workspace. It is the authority the managed-tenant API and SSO use to
// translate a stable external identifier into a numeric tenant id, so the
// SaaS never has to persist WeKnora's internal IDs.
type TenantBinding struct {
	// ID is the database generated primary key.
	ID int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// ExternalSource namespaces the external identifier (e.g. "qh-saas").
	ExternalSource string `json:"external_source" gorm:"type:varchar(64);not null;default:'qh-saas';uniqueIndex:uq_tenant_bindings_external,priority:1"`
	// ExternalTenantID is the stable SaaS-side tenant identifier.
	ExternalTenantID string `json:"external_tenant_id" gorm:"type:varchar(128);not null;uniqueIndex:uq_tenant_bindings_external,priority:2"`
	// TenantID is the provisioned WeKnora workspace.
	TenantID uint64 `json:"tenant_id" gorm:"not null;index"`
	// Status gates SSO and member sync: disabled bindings reject logins with
	// 403 while keeping the workspace and its data intact.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'active'"`
	// CreatedAt / UpdatedAt are managed by gorm.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt soft-deletes the binding; the workspace is untouched.
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// IsActive reports whether the binding currently admits SSO logins.
func (b *TenantBinding) IsActive() bool {
	return b != nil && b.Status == TenantBindingStatusActive
}
