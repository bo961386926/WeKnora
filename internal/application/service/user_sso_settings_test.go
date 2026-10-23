package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ssoSettingsStub resolves only the SSO keys this test cares about; every
// other SystemSettingService method panics via the nil interface embedding.
type ssoSettingsStub struct {
	interfaces.SystemSettingService
	bools   map[string]bool
	strings map[string]string
	ints    map[string]int64
}

func (s *ssoSettingsStub) GetBool(_ context.Context, key, _ string, def bool) bool {
	if v, ok := s.bools[key]; ok {
		return v
	}
	return def
}

func (s *ssoSettingsStub) GetString(_ context.Context, key, _ string, def string) string {
	if v, ok := s.strings[key]; ok {
		return v
	}
	return def
}

func (s *ssoSettingsStub) GetInt(_ context.Context, key, _ string, def int64) int64 {
	if v, ok := s.ints[key]; ok {
		return v
	}
	return def
}

func TestLoadSSOConfigPrefersDatabaseOverEnv(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "false")
	t.Setenv("WEKNORA_SSO_ISSUER", "env-issuer")
	t.Setenv("WEKNORA_SSO_AUDIENCE", "env-audience")
	t.Setenv("WEKNORA_SSO_SECRET", "0123456789abcdef")
	t.Setenv("WEKNORA_SSO_CLOCK_SKEW_SECONDS", "5")

	settings := &ssoSettingsStub{
		bools:   map[string]bool{"sso.enabled": true},
		strings: map[string]string{"sso.issuer": "db-issuer", "sso.audience": "db-audience"},
		ints:    map[string]int64{"sso.clock_skew_seconds": 45},
	}

	cfg, err := loadSSOConfig(context.Background(), settings)
	if err != nil {
		t.Fatalf("loadSSOConfig: %v", err)
	}
	if !cfg.Enabled || cfg.Issuer != "db-issuer" || cfg.Audience != "db-audience" {
		t.Fatalf("database values did not win: %+v", cfg)
	}
	if cfg.ClockSkew != 45*time.Second {
		t.Fatalf("clock skew = %s, want 45s", cfg.ClockSkew)
	}
	// The shared secret intentionally stays an environment-only value.
	if cfg.Secret != "0123456789abcdef" {
		t.Fatalf("secret = %q, want the env value", cfg.Secret)
	}
}

func TestLoadSSOConfigFallsBackToEnv(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_ISSUER", "env-issuer")
	t.Setenv("WEKNORA_SSO_AUDIENCE", "")
	t.Setenv("WEKNORA_SSO_SECRET", "0123456789abcdef")
	t.Setenv("WEKNORA_SSO_CLOCK_SKEW_SECONDS", "12")

	cfg, err := loadSSOConfig(context.Background(), nil)
	if err != nil {
		t.Fatalf("loadSSOConfig: %v", err)
	}
	if !cfg.Enabled || cfg.Issuer != "env-issuer" {
		t.Fatalf("env fallback lost: %+v", cfg)
	}
	if cfg.Audience != "weknora" {
		t.Fatalf("audience = %q, want default weknora", cfg.Audience)
	}
	if cfg.ClockSkew != 12*time.Second {
		t.Fatalf("clock skew = %s, want 12s", cfg.ClockSkew)
	}
}

func TestLoadSSOConfigRejectsShortSecret(t *testing.T) {
	t.Setenv("WEKNORA_SSO_ENABLED", "true")
	t.Setenv("WEKNORA_SSO_SECRET", "too-short")

	if _, err := loadSSOConfig(context.Background(), nil); err == nil {
		t.Fatal("expected a short secret to be rejected when SSO is enabled")
	}
}

func TestSSOClockSkewValidation(t *testing.T) {
	if err := validateRegistryEntry("sso.clock_skew_seconds", int64(301)); err == nil {
		t.Fatal("expected 301 seconds to be rejected")
	}
	if err := validateRegistryEntry("sso.clock_skew_seconds", int64(-1)); err == nil {
		t.Fatal("expected a negative skew to be rejected")
	}
	if err := validateRegistryEntry("sso.clock_skew_seconds", int64(30)); err != nil {
		t.Fatalf("30 seconds must be accepted: %v", err)
	}
}
