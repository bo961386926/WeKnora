package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

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
