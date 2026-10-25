package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Tencent/WeKnora/internal/types"
)

// --- member upsert ---

func upsertFixtures(t *testing.T, active bool) (*ManagedTenantService, *bindingRepoStub, *memberServiceStub, *userRepoStub, *tokenRepoStub, uint64) {
	t.Helper()
	bindings, tenants, members, users, tokens := newStubs()
	status := types.TenantBindingStatusActive
	if !active {
		status = types.TenantBindingStatusDisabled
	}
	tenants.nextID = 1
	tenants.tenants[1] = &types.Tenant{ID: 1, Name: "客户甲", Status: "active"}
	bindings.bindings["qh-saas/tenant-abc"] = &types.TenantBinding{
		ExternalSource: "qh-saas", ExternalTenantID: "tenant-abc",
		TenantID: 1, Status: status,
	}
	svc := newManagedService(bindings, tenants, members, users, tokens)
	return svc, bindings, members, users, tokens, 1
}

func TestUpsertMemberProvisionsUnknownEmail(t *testing.T) {
	svc, _, members, users, tokens, tenantID := upsertFixtures(t, true)

	user, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"new.user@Example.com", types.UpsertMemberRequest{Name: "张三", Role: types.TenantRoleContributor, Active: true})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if user.ID == "" || user.Username == "" {
		t.Fatalf("provisioned user odd: %+v", user)
	}
	if user.Preferences.OidcOnlyLogin == nil || !*user.Preferences.OidcOnlyLogin {
		t.Fatal("provisioned user must be OIDC-only")
	}
	m := members.memberships[user.ID+"@"+strconv.FormatUint(tenantID, 10)]
	if m == nil || m.Role != types.TenantRoleContributor {
		t.Fatalf("membership missing or wrong role: %+v", m)
	}
	if len(tokens.revoked) != 0 {
		t.Fatalf("no revocation expected on first add: %v", tokens.revoked)
	}
	if _, ok := users.users["new.user@example.com"]; !ok {
		t.Fatal("user not persisted under lowercased email")
	}
}

func TestUpsertMemberSyncsRoleAndRevokes(t *testing.T) {
	svc, _, members, users, tokens, tenantID := upsertFixtures(t, true)
	users.users["user@example.com"] = &types.User{ID: "u-1", Email: "user@example.com", IsActive: true}
	members.memberships["u-1@"+strconv.FormatUint(tenantID, 10)] = &types.TenantMember{
		UserID: "u-1", TenantID: tenantID, Role: types.TenantRoleViewer,
		Status: types.TenantMemberStatusActive,
	}

	if _, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"user@example.com", types.UpsertMemberRequest{Role: types.TenantRoleAdmin, Active: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := members.memberships["u-1@"+strconv.FormatUint(tenantID, 10)].Role; got != types.TenantRoleAdmin {
		t.Fatalf("role not synced: %q", got)
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != "u-1" {
		t.Fatalf("role change must revoke sessions: %v", tokens.revoked)
	}
}

func TestUpsertMemberPreservesOwner(t *testing.T) {
	svc, _, members, users, tokens, tenantID := upsertFixtures(t, true)
	users.users["owner@example.com"] = &types.User{ID: "u-owner", Email: "owner@example.com", IsActive: true}
	members.memberships["u-owner@"+strconv.FormatUint(tenantID, 10)] = &types.TenantMember{
		UserID: "u-owner", TenantID: tenantID, Role: types.TenantRoleOwner,
		Status: types.TenantMemberStatusActive,
	}

	if _, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"owner@example.com", types.UpsertMemberRequest{Role: types.TenantRoleViewer, Active: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := members.memberships["u-owner@"+strconv.FormatUint(tenantID, 10)].Role; got != types.TenantRoleOwner {
		t.Fatalf("owner demoted: %q", got)
	}
	if len(tokens.revoked) != 0 {
		t.Fatalf("no revocation expected when owner preserved: %v", tokens.revoked)
	}
}

func TestUpsertMemberInactiveSuspendsAndRevokes(t *testing.T) {
	svc, _, members, users, tokens, tenantID := upsertFixtures(t, true)
	users.users["user@example.com"] = &types.User{ID: "u-1", Email: "user@example.com", IsActive: true}
	members.memberships["u-1@"+strconv.FormatUint(tenantID, 10)] = &types.TenantMember{
		UserID: "u-1", TenantID: tenantID, Role: types.TenantRoleViewer,
		Status: types.TenantMemberStatusActive,
	}

	if _, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"user@example.com", types.UpsertMemberRequest{Role: types.TenantRoleViewer, Active: false}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, ok := members.memberships["u-1@"+strconv.FormatUint(tenantID, 10)]; ok {
		t.Fatal("membership should be removed")
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != "u-1" {
		t.Fatalf("suspension must revoke sessions: %v", tokens.revoked)
	}
}

func TestUpsertMemberRejectsOwnerRoleAndDisabledBinding(t *testing.T) {
	svc, _, _, _, _, _ := upsertFixtures(t, true)
	if _, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"user@example.com", types.UpsertMemberRequest{Role: types.TenantRoleOwner, Active: true}); !errors.Is(err, ErrManagedMemberInvalid) {
		t.Fatalf("expected ErrManagedMemberInvalid, got %v", err)
	}

	svcDisabled, _, _, _, _, _ := upsertFixtures(t, false)
	if _, err := svcDisabled.UpsertMemberByEmail(context.Background(), "qh-saas", "tenant-abc",
		"user@example.com", types.UpsertMemberRequest{Role: types.TenantRoleViewer, Active: true}); !errors.Is(err, ErrManagedMemberDisabled) {
		t.Fatalf("expected ErrManagedMemberDisabled, got %v", err)
	}
}

func TestUpsertMemberUnknownBinding(t *testing.T) {
	svc, _, _, _, _, _ := upsertFixtures(t, true)
	if _, err := svc.UpsertMemberByEmail(context.Background(), "qh-saas", "no-such",
		"user@example.com", types.UpsertMemberRequest{Role: types.TenantRoleViewer, Active: true}); !errors.Is(err, ErrManagedTenantNotFound) {
		t.Fatalf("expected ErrManagedTenantNotFound, got %v", err)
	}
}

// --- replay cache ---

func TestMemoryReplayCacheReplays(t *testing.T) {
	cache := newMemorySSOReplayCache(time.Minute)
	now := time.Now()
	if !cache.Consume("a", now) {
		t.Fatal("first consume must succeed")
	}
	if cache.Consume("a", now) {
		t.Fatal("second consume must fail")
	}
}

func TestRedisReplayCacheReplays(t *testing.T) {
	mr := miniredis.RunT(t)
	cache := &redisSSOReplayCache{
		client: redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		ttl:    time.Minute,
	}
	now := time.Now()
	if !cache.Consume("a", now) {
		t.Fatal("first consume must succeed")
	}
	if cache.Consume("a", now) {
		t.Fatal("second consume must fail")
	}
}

func TestRedisReplayCacheFailsClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	cache := &redisSSOReplayCache{
		client: redis.NewClient(&redis.Options{Addr: addr}),
		ttl:    time.Minute,
	}
	if cache.Consume("a", time.Now()) {
		t.Fatal("unavailable redis must fail closed")
	}
}
