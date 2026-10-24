package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantBindingRepository persists external (SaaS) tenant references and
// their mapping to WeKnora workspaces.
type TenantBindingRepository interface {
	// GetByExternalRef returns the binding for (source, externalID), or
	// (nil, nil) when no active binding exists.
	GetByExternalRef(ctx context.Context, source, externalID string) (*types.TenantBinding, error)
	// GetByTenantID returns the binding pointing at the workspace, or
	// (nil, nil) when none exists.
	GetByTenantID(ctx context.Context, tenantID uint64) (*types.TenantBinding, error)
	// Create inserts a new binding.
	Create(ctx context.Context, binding *types.TenantBinding) error
	// Update persists changes to an existing binding.
	Update(ctx context.Context, binding *types.TenantBinding) error
}
