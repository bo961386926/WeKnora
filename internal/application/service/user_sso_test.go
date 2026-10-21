package service

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
