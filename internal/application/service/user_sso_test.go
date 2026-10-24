package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const ssoTestSecret = "0123456789abcdef0123456789abcdef"

func signSSOToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(ssoTestSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func validSSOClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":    "saas",
		"aud":    "weknora",
		"sub":    "user-1",
		"email":  "User@Example.com",
		"name":   "User One",
		"tenant": float64(10002),
		"role":   "viewer",
		"jti":    "jti-1",
		"iat":    now.Unix(),
		"exp":    now.Add(time.Minute).Unix(),
	}
}

func testSSOConfig() *ssoConfig {
	return &ssoConfig{
		Enabled:   true,
		Secret:    ssoTestSecret,
		Issuer:    "saas",
		Audience:  "weknora",
		ClockSkew: 30 * time.Second,
	}
}

func TestParseSSOTokenValid(t *testing.T) {
	claims, err := parseSSOToken(signSSOToken(t, validSSOClaims()), testSSOConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Fatalf("email not normalised: %q", claims.Email)
	}
	if claims.TenantID != 10002 {
		t.Fatalf("tenant: %d", claims.TenantID)
	}
	if claims.Role != "viewer" {
		t.Fatalf("role: %q", claims.Role)
	}
}

func TestParseSSOTokenRejectsExpired(t *testing.T) {
	claims := validSSOClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	if _, err := parseSSOToken(signSSOToken(t, claims), testSSOConfig()); !errors.Is(err, ErrSSOInvalidToken) {
		t.Fatalf("expected ErrSSOInvalidToken, got %v", err)
	}
}

func TestParseSSOTokenRejectsAudience(t *testing.T) {
	claims := validSSOClaims()
	claims["aud"] = "other"
	if _, err := parseSSOToken(signSSOToken(t, claims), testSSOConfig()); !errors.Is(err, ErrSSOInvalidToken) {
		t.Fatalf("expected ErrSSOInvalidToken, got %v", err)
	}
}

func TestParseSSOTokenRejectsIssuer(t *testing.T) {
	claims := validSSOClaims()
	claims["iss"] = "someone-else"
	if _, err := parseSSOToken(signSSOToken(t, claims), testSSOConfig()); !errors.Is(err, ErrSSOInvalidToken) {
		t.Fatalf("expected ErrSSOInvalidToken, got %v", err)
	}
}

func TestParseSSOTokenRejectsOwnerRole(t *testing.T) {
	claims := validSSOClaims()
	claims["role"] = "owner"
	if _, err := parseSSOToken(signSSOToken(t, claims), testSSOConfig()); !errors.Is(err, ErrSSORoleNotAllowed) {
		t.Fatalf("expected ErrSSORoleNotAllowed, got %v", err)
	}
}

func TestParseSSOTokenRejectsBadSignature(t *testing.T) {
	cfg := testSSOConfig()
	cfg.Secret = "ffffffffffffffffffffffffffffffff"
	if _, err := parseSSOToken(signSSOToken(t, validSSOClaims()), cfg); !errors.Is(err, ErrSSOInvalidToken) {
		t.Fatalf("expected ErrSSOInvalidToken, got %v", err)
	}
}

func TestParseSSOTokenRejectsLongLived(t *testing.T) {
	claims := validSSOClaims()
	claims["iat"] = time.Now().Add(-time.Hour).Unix()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	if _, err := parseSSOToken(signSSOToken(t, claims), testSSOConfig()); !errors.Is(err, ErrSSOInvalidToken) {
		t.Fatalf("expected ErrSSOInvalidToken, got %v", err)
	}
}

func TestConsumeJTIRejectsReplay(t *testing.T) {
	now := time.Now()
	if !consumeJTI("replay-test", now) {
		t.Fatal("first use should be accepted")
	}
	if consumeJTI("replay-test", now) {
		t.Fatal("second use should be rejected")
	}
}

// ssoMemberStub records how ensureSSOMembership interacts with the
// membership service. Every other method panics via the nil embedding.
type ssoMemberStub struct {
	interfaces.TenantMemberService
	membership  *types.TenantMember
	addedRole   types.TenantRole
	added       int
	updatedRole types.TenantRole
	updated     int
}

func (s *ssoMemberStub) GetMembership(context.Context, string, uint64) (*types.TenantMember, error) {
	return s.membership, nil
}

func (s *ssoMemberStub) AddMember(
	_ context.Context, _ string, _ uint64, role types.TenantRole, _ *string,
) (*types.TenantMember, error) {
	s.added++
	s.addedRole = role
	return &types.TenantMember{Role: role}, nil
}

func (s *ssoMemberStub) UpdateRole(_ context.Context, _ string, _ uint64, role types.TenantRole) error {
	s.updated++
	s.updatedRole = role
	return nil
}

func TestEnsureSSOMembershipAddsMissingMember(t *testing.T) {
	stub := &ssoMemberStub{}
	svc := &userService{memberService: stub}

	if err := svc.ensureSSOMembership(context.Background(), "u1", 10002, types.TenantRoleViewer); err != nil {
		t.Fatalf("ensureSSOMembership: %v", err)
	}
	if stub.added != 1 || stub.addedRole != types.TenantRoleViewer {
		t.Fatalf("AddMember calls=%d role=%q, want 1 viewer", stub.added, stub.addedRole)
	}
	if stub.updated != 0 {
		t.Fatalf("UpdateRole calls=%d, want 0", stub.updated)
	}
}

func TestEnsureSSOMembershipSyncsChangedRole(t *testing.T) {
	stub := &ssoMemberStub{membership: &types.TenantMember{
		Role:   types.TenantRoleViewer,
		Status: types.TenantMemberStatusActive,
	}}
	svc := &userService{memberService: stub}

	if err := svc.ensureSSOMembership(context.Background(), "u1", 10002, types.TenantRoleAdmin); err != nil {
		t.Fatalf("ensureSSOMembership: %v", err)
	}
	if stub.updated != 1 || stub.updatedRole != types.TenantRoleAdmin {
		t.Fatalf("UpdateRole calls=%d role=%q, want 1 admin", stub.updated, stub.updatedRole)
	}
	if stub.added != 0 {
		t.Fatalf("AddMember calls=%d, want 0", stub.added)
	}
}

func TestEnsureSSOMembershipLeavesMatchingRoleUntouched(t *testing.T) {
	stub := &ssoMemberStub{membership: &types.TenantMember{
		Role:   types.TenantRoleViewer,
		Status: types.TenantMemberStatusActive,
	}}
	svc := &userService{memberService: stub}

	if err := svc.ensureSSOMembership(context.Background(), "u1", 10002, types.TenantRoleViewer); err != nil {
		t.Fatalf("ensureSSOMembership: %v", err)
	}
	if stub.updated != 0 || stub.added != 0 {
		t.Fatalf("unexpected write: added=%d updated=%d", stub.added, stub.updated)
	}
}

func TestEnsureSSOMembershipPreservesWorkspaceOwner(t *testing.T) {
	stub := &ssoMemberStub{membership: &types.TenantMember{
		Role:   types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}}
	svc := &userService{memberService: stub}

	if err := svc.ensureSSOMembership(context.Background(), "u1", 10002, types.TenantRoleAdmin); err != nil {
		t.Fatalf("ensureSSOMembership: %v", err)
	}
	if stub.updated != 0 {
		t.Fatalf("owner membership must not be demoted by SSO, UpdateRole calls=%d", stub.updated)
	}
}

// --- external tenant reference resolution (P1 managed tenants) ---

type ssoUserRepoStub struct {
	interfaces.UserRepository
	users       map[string]*types.User
	updated     int
	fallbackErr error
}

func (s *ssoUserRepoStub) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if s.fallbackErr != nil {
		return nil, s.fallbackErr
	}
	if u, ok := s.users[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, apprepo.ErrUserNotFound
}

func (s *ssoUserRepoStub) UpdateUser(_ context.Context, u *types.User) error {
	s.updated++
	return nil
}

func (s *ssoUserRepoStub) GetUserByUsername(_ context.Context, _ string) (*types.User, error) {
	return nil, apprepo.ErrUserNotFound
}

func (s *ssoUserRepoStub) CreateUser(_ context.Context, u *types.User) error {
	u.ID = "u-" + strconv.Itoa(len(s.users)+1)
	s.users[strings.ToLower(u.Email)] = u
	return nil
}

type ssoTenantServiceStub struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
}

func (s *ssoTenantServiceStub) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return s.tenants[id], nil
}

type ssoMemberStub2 struct {
	interfaces.TenantMemberService
	memberships map[string]*types.TenantMember
	added       []struct {
		userID string
		tenant uint64
		role   types.TenantRole
	}
}

func (s *ssoMemberStub2) key(userID string, tenantID uint64) string {
	return userID + "@" + strconv.FormatUint(tenantID, 10)
}

func (s *ssoMemberStub2) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	return s.memberships[s.key(userID, tenantID)], nil
}

func (s *ssoMemberStub2) AddMember(_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string) (*types.TenantMember, error) {
	m := &types.TenantMember{UserID: userID, TenantID: tenantID, Role: role, Status: types.TenantMemberStatusActive}
	s.memberships[s.key(userID, tenantID)] = m
	s.added = append(s.added, struct {
		userID string
		tenant uint64
		role   types.TenantRole
	}{userID, tenantID, role})
	return m, nil
}

func (s *ssoMemberStub2) UpdateRole(_ context.Context, _ string, tenantID uint64, role types.TenantRole) error {
	for key, m := range s.memberships {
		if m.TenantID == tenantID {
			m.Role = role
			s.memberships[key] = m
		}
	}
	return nil
}

func (s *ssoMemberStub2) ListByUser(_ context.Context, userID string) ([]*types.TenantMember, error) {
	var out []*types.TenantMember
	for _, m := range s.memberships {
		if m.UserID == userID {
			out = append(out, m)
		}
	}
	return out, nil
}

func newSSOLoginService(
	bindings *bindingRepoStub,
	tenants *ssoTenantServiceStub,
	members *ssoMemberStub2,
	users *ssoUserRepoStub,
) *userService {
	return &userService{
		userRepo:      users,
		tokenRepo:     &tokenRepoStub{},
		tenantService: tenants,
		memberService: members,
		bindingRepo:   bindings,
	}
}

func ssoLoginClaims(email string, tenant any, role string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "saas", "aud": "weknora", "sub": "saas-u1",
		"email": email, "name": "U", "tenant": tenant, "role": role,
		"jti": uuid.NewString(), "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	}
}

func TestLoginWithSSOExternalTenantRefProvisionsMember(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", ssoTestSecret)
	bindings := &bindingRepoStub{bindings: map[string]*types.TenantBinding{}}
	tenants := &ssoTenantServiceStub{tenants: map[uint64]*types.Tenant{
		10002: {ID: 10002, Name: "客户甲", Status: "active"},
	}}
	members := &ssoMemberStub2{memberships: map[string]*types.TenantMember{}}
	users := &ssoUserRepoStub{users: map[string]*types.User{}}
	bindings.bindings["qh-saas/tenant-abc"] = &types.TenantBinding{
		ExternalSource: "qh-saas", ExternalTenantID: "tenant-abc",
		TenantID: 10002, Status: types.TenantBindingStatusActive,
	}
	svc := newSSOLoginService(bindings, tenants, members, users)

	resp, err := svc.LoginWithSSO(context.Background(),
		signSSOToken(t, ssoLoginClaims("New.User@Example.com", "tenant-abc", "viewer")))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !resp.Success || resp.ActiveTenant == nil || resp.ActiveTenant.ID != 10002 {
		t.Fatalf("bad response: %+v", resp)
	}
	if len(members.added) != 1 || members.added[0].tenant != 10002 || members.added[0].role != types.TenantRoleViewer {
		t.Fatalf("membership not provisioned: %+v", members.added)
	}
}

func TestLoginWithSSODisabledBindingRejected(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", ssoTestSecret)
	bindings := &bindingRepoStub{bindings: map[string]*types.TenantBinding{}}
	tenants := &ssoTenantServiceStub{tenants: map[uint64]*types.Tenant{
		10002: {ID: 10002, Name: "客户甲", Status: "active"},
	}}
	svc := newSSOLoginService(bindings, tenants,
		&ssoMemberStub2{memberships: map[string]*types.TenantMember{}},
		&ssoUserRepoStub{users: map[string]*types.User{}})
	bindings.bindings["qh-saas/tenant-abc"] = &types.TenantBinding{
		ExternalSource: "qh-saas", ExternalTenantID: "tenant-abc",
		TenantID: 10002, Status: types.TenantBindingStatusDisabled,
	}

	_, err := svc.LoginWithSSO(context.Background(),
		signSSOToken(t, ssoLoginClaims("user@example.com", "tenant-abc", "viewer")))
	if !errors.Is(err, ErrSSOBindingDisabled) {
		t.Fatalf("expected ErrSSOBindingDisabled, got %v", err)
	}
}

func TestLoginWithSSOUnknownExternalRef(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", ssoTestSecret)
	svc := newSSOLoginService(
		&bindingRepoStub{bindings: map[string]*types.TenantBinding{}},
		&ssoTenantServiceStub{tenants: map[uint64]*types.Tenant{}},
		&ssoMemberStub2{memberships: map[string]*types.TenantMember{}},
		&ssoUserRepoStub{users: map[string]*types.User{}})

	_, err := svc.LoginWithSSO(context.Background(),
		signSSOToken(t, ssoLoginClaims("user@example.com", "tenant-xyz", "viewer")))
	if !errors.Is(err, ErrSSOTenantNotFound) {
		t.Fatalf("expected ErrSSOTenantNotFound, got %v", err)
	}
}

func TestLoginWithSSONumericTenantStillWorks(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", ssoTestSecret)
	tenants := &ssoTenantServiceStub{tenants: map[uint64]*types.Tenant{
		10002: {ID: 10002, Name: "客户甲", Status: "active"},
	}}
	members := &ssoMemberStub2{memberships: map[string]*types.TenantMember{}}
	users := &ssoUserRepoStub{users: map[string]*types.User{
		"user@example.com": {ID: "u-1", Email: "user@example.com", IsActive: true},
	}}
	// Binding repo empty: numeric claims must not consult it.
	svc := newSSOLoginService(
		&bindingRepoStub{bindings: map[string]*types.TenantBinding{}},
		tenants, members, users)

	resp, err := svc.LoginWithSSO(context.Background(),
		signSSOToken(t, ssoLoginClaims("user@example.com", float64(10002), "admin")))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.ActiveTenant == nil || resp.ActiveTenant.ID != 10002 {
		t.Fatalf("numeric claim path broken: %+v", resp)
	}
}

func TestLoginWithSSOExistingMemberRoleSynced(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", ssoTestSecret)
	tenants := &ssoTenantServiceStub{tenants: map[uint64]*types.Tenant{
		10002: {ID: 10002, Name: "客户甲", Status: "active"},
	}}
	members := &ssoMemberStub2{memberships: map[string]*types.TenantMember{
		"u-1@10002": {UserID: "u-1", TenantID: 10002, Role: types.TenantRoleViewer,
			Status: types.TenantMemberStatusActive},
	}}
	users := &ssoUserRepoStub{users: map[string]*types.User{
		"user@example.com": {ID: "u-1", Email: "user@example.com", IsActive: true},
	}}
	svc := newSSOLoginService(
		&bindingRepoStub{bindings: map[string]*types.TenantBinding{}},
		tenants, members, users)

	if _, err := svc.LoginWithSSO(context.Background(),
		signSSOToken(t, ssoLoginClaims("user@example.com", float64(10002), "contributor"))); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := members.memberships["u-1@10002"].Role; got != types.TenantRoleContributor {
		t.Fatalf("role not synced: %q", got)
	}
}
