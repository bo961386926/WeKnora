package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// --- stubs ---

type bindingRepoStub struct {
	bindings map[string]*types.TenantBinding // key: source + "/" + externalID
	created  int
	updated  int
	failNext bool
}

func (s *bindingRepoStub) key(source, externalID string) string {
	return source + "/" + externalID
}

func (s *bindingRepoStub) GetByExternalRef(_ context.Context, source, externalID string) (*types.TenantBinding, error) {
	if s.failNext {
		return nil, errors.New("db down")
	}
	return s.bindings[s.key(source, externalID)], nil
}

func (s *bindingRepoStub) GetByTenantID(_ context.Context, tenantID uint64) (*types.TenantBinding, error) {
	for _, b := range s.bindings {
		if b.TenantID == tenantID {
			return b, nil
		}
	}
	return nil, nil
}

func (s *bindingRepoStub) Create(_ context.Context, binding *types.TenantBinding) error {
	if s.failNext {
		return errors.New("db down")
	}
	s.created++
	s.bindings[s.key(binding.ExternalSource, binding.ExternalTenantID)] = binding
	return nil
}

func (s *bindingRepoStub) Update(_ context.Context, binding *types.TenantBinding) error {
	s.updated++
	s.bindings[s.key(binding.ExternalSource, binding.ExternalTenantID)] = binding
	return nil
}

type tenantServiceStub struct {
	interfaces.TenantService
	tenants  map[uint64]*types.Tenant
	nextID   uint64
	updated  int
	failNext bool
}

func (s *tenantServiceStub) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return s.tenants[id], nil
}

func (s *tenantServiceStub) CreateTenant(_ context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	if s.failNext {
		return nil, errors.New("db down")
	}
	s.nextID++
	tenant.ID = s.nextID
	s.tenants[tenant.ID] = tenant
	return tenant, nil
}

func (s *tenantServiceStub) UpdateTenant(_ context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	s.updated++
	s.tenants[tenant.ID] = tenant
	return tenant, nil
}

// memberRepoStub satisfies the pieces of TenantMemberService used here via
// embedding: only GetMembership / RemoveMember are called by the service.
type memberServiceStub struct {
	interfaces.TenantMemberService
	memberships map[string]*types.TenantMember // key: userID + "@" + tenantID
	removed     []string
}

func (s *memberServiceStub) key(userID string, tenantID uint64) string {
	return userID + "@" + strconv.FormatUint(tenantID, 10)
}

func (s *memberServiceStub) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	return s.memberships[s.key(userID, tenantID)], nil
}

func (s *memberServiceStub) RemoveMember(_ context.Context, userID string, tenantID uint64) error {
	key := s.key(userID, tenantID)
	if _, ok := s.memberships[key]; !ok {
		return errors.New("membership not found")
	}
	delete(s.memberships, key)
	s.removed = append(s.removed, userID)
	return nil
}

type userRepoStub struct {
	interfaces.UserRepository
	users map[string]*types.User // key: email
}

func (s *userRepoStub) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	return s.users[strings.ToLower(email)], nil
}

type tokenRepoStub struct {
	interfaces.AuthTokenRepository
	revoked []string
}

func (s *tokenRepoStub) CreateToken(context.Context, *types.AuthToken) error { return nil }

func (s *tokenRepoStub) RevokeTokensByUserID(_ context.Context, userID string) error {
	s.revoked = append(s.revoked, userID)
	return nil
}

// --- fixtures ---

func newManagedService(
	bindings *bindingRepoStub,
	tenants *tenantServiceStub,
	members *memberServiceStub,
	users *userRepoStub,
	tokens *tokenRepoStub,
) *ManagedTenantService {
	return &ManagedTenantService{
		bindingRepo:   bindings,
		tenantService: tenants,
		memberService: members,
		userRepo:      users,
		tokenRepo:     tokens,
	}
}

func newStubs() (*bindingRepoStub, *tenantServiceStub, *memberServiceStub, *userRepoStub, *tokenRepoStub) {
	return &bindingRepoStub{bindings: map[string]*types.TenantBinding{}},
		&tenantServiceStub{tenants: map[uint64]*types.Tenant{}},
		&memberServiceStub{memberships: map[string]*types.TenantMember{}},
		&userRepoStub{users: map[string]*types.User{}},
		&tokenRepoStub{}
}

// --- provisioning ---

func TestProvisionManagedTenantCreatesWorkspaceAndBinding(t *testing.T) {
	bindings, tenants, _, _, _ := newStubs()
	svc := newManagedService(bindings, tenants, nil, nil, nil)

	tenant, created, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "客户甲", true)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !created || tenant.ID == 0 {
		t.Fatalf("created=%t id=%d", created, tenant.ID)
	}
	binding := bindings.bindings["qh-saas/tenant-abc"]
	if binding == nil || binding.TenantID != tenant.ID || binding.Status != types.TenantBindingStatusActive {
		t.Fatalf("binding not written correctly: %+v", binding)
	}
}

func TestProvisionManagedTenantIsIdempotent(t *testing.T) {
	bindings, tenants, _, _, _ := newStubs()
	svc := newManagedService(bindings, tenants, nil, nil, nil)

	first, created1, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "客户甲", true)
	if err != nil || !created1 {
		t.Fatalf("first provision: created=%t err=%v", created1, err)
	}
	second, created2, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "客户甲", true)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if created2 {
		t.Fatal("second call must not create a new workspace")
	}
	if second.ID != first.ID {
		t.Fatalf("workspace ids differ: %d vs %d", first.ID, second.ID)
	}
	if bindings.created != 1 || tenants.nextID != 1 {
		t.Fatalf("unexpected writes: bindings=%d tenants=%d", bindings.created, tenants.nextID)
	}
}

func TestProvisionManagedTenantRenamesAndDisables(t *testing.T) {
	bindings, tenants, _, _, _ := newStubs()
	svc := newManagedService(bindings, tenants, nil, nil, nil)

	if _, _, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "旧名", true); err != nil {
		t.Fatalf("provision: %v", err)
	}

	updated, created, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "新名", false)
	if err != nil || created {
		t.Fatalf("update: created=%t err=%v", created, err)
	}
	if updated.Name != "新名" {
		t.Fatalf("rename failed: %q", updated.Name)
	}
	if tenants.updated != 1 {
		t.Fatalf("workspace rename writes=%d", tenants.updated)
	}
	if bindings.bindings["qh-saas/tenant-abc"].Status != types.TenantBindingStatusDisabled {
		t.Fatal("binding should be disabled")
	}
}

func TestProvisionManagedTenantRequiresNameOnCreate(t *testing.T) {
	bindings, tenants, _, _, _ := newStubs()
	svc := newManagedService(bindings, tenants, nil, nil, nil)

	if _, _, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "tenant-abc", "  ", true); !errors.Is(err, ErrManagedTenantInvalid) {
		t.Fatalf("expected ErrManagedTenantInvalid, got %v", err)
	}
	if _, _, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "", "x", true); !errors.Is(err, ErrManagedTenantInvalid) {
		t.Fatalf("expected ErrManagedTenantInvalid for empty ref, got %v", err)
	}
	if tenants.nextID != 0 {
		t.Fatal("no workspace should be created")
	}
}

func TestProvisionManagedTenantDanglingBinding(t *testing.T) {
	bindings, tenants, _, _, _ := newStubs()
	bindings.bindings["qh-saas/ghost"] = &types.TenantBinding{
		ExternalSource:   "qh-saas",
		ExternalTenantID: "ghost",
		TenantID:         999,
		Status:           types.TenantBindingStatusActive,
	}
	svc := newManagedService(bindings, tenants, nil, nil, nil)

	if _, _, err := svc.ProvisionManagedTenant(
		context.Background(), "qh-saas", "ghost", "x", true); !errors.Is(err, ErrManagedTenantUnusable) {
		t.Fatalf("expected ErrManagedTenantUnusable, got %v", err)
	}
}

// --- member removal ---

func memberFixtures(t *testing.T) (*ManagedTenantService, *bindingRepoStub, *memberServiceStub, *tokenRepoStub) {
	t.Helper()
	bindings, tenants, members, users, tokens := newStubs()
	tenant, _, err := newManagedService(bindings, tenants, members, users, tokens).
		ProvisionManagedTenant(context.Background(), "qh-saas", "tenant-abc", "客户甲", true)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	_ = tenant
	users.users["user@example.com"] = &types.User{ID: "u-1", Email: "user@example.com", IsActive: true}
	members.memberships["u-1@"+strconv.FormatUint(tenant.ID, 10)] = &types.TenantMember{
		UserID: "u-1", TenantID: tenant.ID, Role: types.TenantRoleViewer,
		Status: types.TenantMemberStatusActive,
	}
	svc := newManagedService(bindings, tenants, members, users, tokens)
	return svc, bindings, members, tokens
}

func TestRemoveMemberByEmailRemovesAndRevokes(t *testing.T) {
	svc, bindings, members, tokens := memberFixtures(t)

	if err := svc.RemoveMemberByEmail(
		context.Background(), "qh-saas", "tenant-abc", "User@Example.com"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(members.removed) != 1 || members.removed[0] != "u-1" {
		t.Fatalf("removals: %v", members.removed)
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != "u-1" {
		t.Fatalf("revocations: %v", tokens.revoked)
	}
	if bindings.bindings["qh-saas/tenant-abc"] == nil {
		t.Fatal("binding must remain")
	}
}

func TestRemoveMemberByEmailUnknownUserOrBinding(t *testing.T) {
	svc, _, _, _ := memberFixtures(t)

	if err := svc.RemoveMemberByEmail(
		context.Background(), "qh-saas", "tenant-abc", "ghost@example.com"); !errors.Is(err, ErrManagedUserNotFound) {
		t.Fatalf("expected ErrManagedUserNotFound, got %v", err)
	}
	if err := svc.RemoveMemberByEmail(
		context.Background(), "qh-saas", "no-such", "user@example.com"); !errors.Is(err, ErrManagedTenantNotFound) {
		t.Fatalf("expected ErrManagedTenantNotFound, got %v", err)
	}
}
