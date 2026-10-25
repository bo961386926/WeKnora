package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ManagedTenantService provisions workspaces for external (SaaS) tenant
// references and keeps the mapping in sync. Every operation is idempotent so
// the SaaS event worker can retry freely.
type ManagedTenantService interface {
	// ProvisionManagedTenant creates or updates the workspace bound to
	// (source, externalRef) and returns it plus whether it was created.
	ProvisionManagedTenant(
		ctx context.Context, source, externalRef, name string, active bool,
	) (*types.Tenant, bool, error)
	// RemoveMemberByEmail removes the email-identified user from the bound
	// workspace and revokes their sessions.
	RemoveMemberByEmail(ctx context.Context, source, externalRef, email string) error
	// UpsertMemberByEmail creates or updates one member of the bound
	// workspace: unknown emails are provisioned, roles are synced with
	// session revocation on change, and active=false suspends membership.
	UpsertMemberByEmail(
		ctx context.Context, source, externalRef, email string, req types.UpsertMemberRequest,
	) (*types.User, error)
}
