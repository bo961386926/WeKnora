package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Managed tenant errors mapped by the handler to 4xx codes.
var (
	// ErrManagedTenantInvalid means the external ref or name failed validation.
	ErrManagedTenantInvalid = errors.New("invalid managed tenant request")
	// ErrManagedTenantNotFound means the external reference has no binding.
	ErrManagedTenantNotFound = errors.New("managed tenant not found")
	// ErrManagedTenantUnusable means the binding points at a missing workspace.
	ErrManagedTenantUnusable = errors.New("managed tenant binding is dangling")
	// ErrManagedUserNotFound means the external user has no WeKnora account.
	ErrManagedUserNotFound = errors.New("managed user not found")
	// ErrManagedUserNotMember means the user has no membership to remove.
	ErrManagedUserNotMember = errors.New("user is not a member of the managed workspace")
)

const (
	maxExternalRefLength = 128
	maxManagedNameLength = 128
)

// ManagedTenantService provisions workspaces on behalf of the SaaS platform
// and keeps the external-reference → workspace mapping. All operations are
// idempotent so the SaaS delivery worker can retry freely.
type ManagedTenantService struct {
	bindingRepo   interfaces.TenantBindingRepository
	tenantService interfaces.TenantService
	memberService interfaces.TenantMemberService
	userRepo      interfaces.UserRepository
	tokenRepo     interfaces.AuthTokenRepository
}

// NewManagedTenantService wires the managed tenant service.
func NewManagedTenantService(
	bindingRepo interfaces.TenantBindingRepository,
	tenantService interfaces.TenantService,
	memberService interfaces.TenantMemberService,
	userRepo interfaces.UserRepository,
	tokenRepo interfaces.AuthTokenRepository,
) *ManagedTenantService {
	return &ManagedTenantService{
		bindingRepo:   bindingRepo,
		tenantService: tenantService,
		memberService: memberService,
		userRepo:      userRepo,
		tokenRepo:     tokenRepo,
	}
}

// sanitizeExternalRef validates an external tenant reference. References are
// opaque strings; only emptiness and length are policed so the SaaS can use
// UUIDs, slugs or numeric ids alike.
func sanitizeExternalRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > maxExternalRefLength {
		return "", ErrManagedTenantInvalid
	}
	return ref, nil
}

// ProvisionManagedTenant creates or updates the workspace bound to
// (source, externalRef). Returns the workspace and whether it was created.
//
// Idempotency: an existing binding is updated in place — name changes are
// pushed to the workspace, the binding status follows `active`. The
// workspace row itself is never disabled or deleted here; disabling the
// binding alone already blocks SSO while keeping data intact.
func (s *ManagedTenantService) ProvisionManagedTenant(
	ctx context.Context, source, externalRef, name string, active bool,
) (*types.Tenant, bool, error) {
	ref, err := sanitizeExternalRef(externalRef)
	if err != nil {
		return nil, false, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = types.DefaultTenantBindingSource
	}
	name = strings.TrimSpace(name)

	binding, err := s.bindingRepo.GetByExternalRef(ctx, source, ref)
	if err != nil {
		return nil, false, err
	}

	if binding != nil {
		tenant, err := s.tenantService.GetTenantByID(ctx, binding.TenantID)
		if err != nil || tenant == nil {
			logger.Errorf(ctx, "[managed] binding %s/%s points at missing workspace %d",
				source, ref, binding.TenantID)
			return nil, false, ErrManagedTenantUnusable
		}
		if name != "" && name != tenant.Name {
			tenant.Name = name
			if _, err := s.tenantService.UpdateTenant(ctx, tenant); err != nil {
				return nil, false, err
			}
		}
		newStatus := types.TenantBindingStatusActive
		if !active {
			newStatus = types.TenantBindingStatusDisabled
		}
		if binding.Status != newStatus {
			binding.Status = newStatus
			if err := s.bindingRepo.Update(ctx, binding); err != nil {
				return nil, false, err
			}
		}
		logger.Infof(ctx, "[managed] workspace %d updated for %s/%s (active=%t)",
			binding.TenantID, source, ref, active)
		return tenant, false, nil
	}

	if name == "" {
		return nil, false, fmt.Errorf("%w: name is required when creating a workspace",
			ErrManagedTenantInvalid)
	}
	if len(name) > maxManagedNameLength {
		return nil, false, fmt.Errorf("%w: name exceeds %d characters", ErrManagedTenantInvalid, maxManagedNameLength)
	}

	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{
		Name:        name,
		Description: fmt.Sprintf("Provisioned for external tenant %s", ref),
	})
	if err != nil {
		return nil, false, err
	}

	newStatus := types.TenantBindingStatusActive
	if !active {
		newStatus = types.TenantBindingStatusDisabled
	}
	binding = &types.TenantBinding{
		ExternalSource:   source,
		ExternalTenantID: ref,
		TenantID:         tenant.ID,
		Status:           newStatus,
	}
	if err := s.bindingRepo.Create(ctx, binding); err != nil {
		// Leave the created workspace in place; a retry of the same event
		// finds the missing binding and recreates it rather than a second
		// workspace only if CreateTenant is also retried. Surface the error
		// so the SaaS worker retries the whole idempotent step.
		return nil, false, err
	}
	logger.Infof(ctx, "[managed] workspace %d created for %s/%s (active=%t)",
		tenant.ID, source, ref, active)
	return tenant, true, nil
}

// RemoveMemberByEmail removes the user identified by email from the
// workspace bound to (source, externalRef) and revokes their sessions so a
// removal or demotion takes effect immediately.
func (s *ManagedTenantService) RemoveMemberByEmail(
	ctx context.Context, source, externalRef, email string,
) error {
	ref, err := sanitizeExternalRef(externalRef)
	if err != nil {
		return err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = types.DefaultTenantBindingSource
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ErrManagedUserNotFound
	}

	binding, err := s.bindingRepo.GetByExternalRef(ctx, source, ref)
	if err != nil {
		return err
	}
	if binding == nil {
		return ErrManagedTenantNotFound
	}

	user, err := s.userRepo.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		return ErrManagedUserNotFound
	}

	membership, err := s.memberService.GetMembership(ctx, user.ID, binding.TenantID)
	if err != nil {
		return err
	}
	if membership == nil {
		return ErrManagedUserNotMember
	}

	if err := s.memberService.RemoveMember(ctx, user.ID, binding.TenantID); err != nil {
		return err
	}
	if err := s.tokenRepo.RevokeTokensByUserID(ctx, user.ID); err != nil {
		// The membership row is already gone; a failed revocation must not
		// make the SaaS retry resurrect anything. Log loudly instead.
		logger.Errorf(ctx, "[managed] removed user %s from workspace %d but session revocation failed: %v",
			user.ID, binding.TenantID, err)
	}
	logger.Infof(ctx, "[managed] removed user %s from workspace %d (%s/%s)",
		user.ID, binding.TenantID, source, ref)
	return nil
}
