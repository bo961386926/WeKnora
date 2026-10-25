package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Managed member sync errors mapped by the handler.
var (
	// ErrManagedMemberDisabled means the bound workspace or the account is
	// suspended; the handler maps it to 403.
	ErrManagedMemberDisabled = errors.New("managed member target is disabled")
	// ErrManagedMemberInvalid means the role or payload failed validation.
	ErrManagedMemberInvalid = errors.New("invalid managed member request")
)

// UpsertMemberByEmail creates or updates one member of the workspace bound
// to (source, externalRef). The external user reference is the account
// email: unknown emails are provisioned as OIDC-style accounts (random
// password, no local login) with no workspace of their own. Role changes on
// an active member revoke their sessions so a demotion takes effect
// immediately; active=false removes the membership and revokes sessions.
func (s *ManagedTenantService) UpsertMemberByEmail(
	ctx context.Context, source, externalRef, email string, req types.UpsertMemberRequest,
) (*types.User, error) {
	ref, err := sanitizeExternalRef(externalRef)
	if err != nil {
		return nil, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = types.DefaultTenantBindingSource
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrManagedUserNotFound
	}
	if !req.Role.IsValid() || req.Role == types.TenantRoleOwner {
		return nil, fmt.Errorf("%w: role must be viewer/contributor/admin", ErrManagedMemberInvalid)
	}

	binding, err := s.bindingRepo.GetByExternalRef(ctx, source, ref)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, ErrManagedTenantNotFound
	}
	if !binding.IsActive() {
		return nil, ErrManagedMemberDisabled
	}

	user, err := s.userRepo.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		user, err = s.provisionUserByEmail(ctx, email, req.Name)
		if err != nil {
			return nil, err
		}
	}
	if !user.IsActive {
		return nil, ErrManagedMemberDisabled
	}
	if req.Name != "" && req.Name != user.Username {
		// sanitizeUsernameCandidate strips non-ASCII (CJK names become
		// empty); keep the existing username rather than blanking it.
		if candidate := sanitizeUsernameCandidate(req.Name); candidate != "" && candidate != user.Username {
			user.Username = candidate
			user.UpdatedAt = time.Now()
			if err := s.userRepo.UpdateUser(ctx, user); err != nil {
				return nil, err
			}
		}
	}

	membership, err := s.memberService.GetMembership(ctx, user.ID, binding.TenantID)
	if err != nil {
		return nil, err
	}

	if !req.Active {
		if membership == nil {
			return user, nil
		}
		if err := s.memberService.RemoveMember(ctx, user.ID, binding.TenantID); err != nil {
			return nil, err
		}
		if err := s.tokenRepo.RevokeTokensByUserID(ctx, user.ID); err != nil {
			logger.Errorf(ctx, "[managed] suspended user %s in workspace %d but session revocation failed: %v",
				user.ID, binding.TenantID, err)
		}
		logger.Infof(ctx, "[managed] suspended user %s in workspace %d (%s/%s)",
			user.ID, binding.TenantID, source, ref)
		return user, nil
	}

	if membership == nil {
		if _, err := s.memberService.AddMember(ctx, user.ID, binding.TenantID, req.Role, nil); err != nil {
			return nil, err
		}
		logger.Infof(ctx, "[managed] added user %s to workspace %d as %s (%s/%s)",
			user.ID, binding.TenantID, req.Role, source, ref)
		return user, nil
	}

	// Existing member: sync the role, mirroring the SSO rule that a local
	// workspace Owner is never demoted through the managed API. Sessions are
	// only revoked on an actual role change — preserving an owner is a no-op.
	if membership.Role != req.Role {
		if membership.Role == types.TenantRoleOwner {
			logger.Warnf(ctx, "[managed] keeping workspace owner role for user %s in workspace %d (requested %s)",
				user.ID, binding.TenantID, req.Role)
		} else {
			if err := s.memberService.UpdateRole(ctx, user.ID, binding.TenantID, req.Role); err != nil {
				return nil, err
			}
			if err := s.tokenRepo.RevokeTokensByUserID(ctx, user.ID); err != nil {
				logger.Errorf(ctx, "[managed] role change for user %s in workspace %d but session revocation failed: %v",
					user.ID, binding.TenantID, err)
			}
		}
	}
	return user, nil
}

// provisionUserByEmail creates an OIDC-style account: random unusable
// password (no local login), tenantless home, display name seeded from the
// email local-part unless a name is supplied.
func (s *ManagedTenantService) provisionUserByEmail(
	ctx context.Context, email, name string,
) (*types.User, error) {
	password, err := generateRandomString(32)
	if err != nil {
		return nil, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	base := sanitizeUsernameCandidate(name)
	if base == "" {
		base = sanitizeUsernameCandidate(strings.Split(email, "@")[0])
	}
	if base == "" {
		base = "managed-user"
	}
	username := base
	for i := 0; i < 20; i++ {
		existing, lookupErr := s.userRepo.GetUserByUsername(ctx, username)
		if lookupErr != nil || existing == nil {
			break
		}
		username = fmt.Sprintf("%s-%s", base, uuid.NewString()[:8])
	}

	oidcOnly := true
	user := &types.User{
		ID:           uuid.New().String(),
		Username:     username,
		Email:        email,
		PasswordHash: string(hashed),
		TenantID:     0,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	user.Preferences.OidcOnlyLogin = &oidcOnly
	if err := s.userRepo.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	logger.Infof(ctx, "[managed] provisioned user %s (%s)", user.ID, email)
	return user, nil
}
