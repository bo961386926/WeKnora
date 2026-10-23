package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// SSO errors surfaced by the handler as 401/403/404.
var (
	ErrSSODisabled       = errors.New("sso is not enabled")
	ErrSSOInvalidToken   = errors.New("invalid sso token")
	ErrSSOTenantNotFound = errors.New("sso target workspace not found")
	ErrSSOUserDisabled   = errors.New("sso user is disabled")
	ErrSSORoleNotAllowed = errors.New("sso role is not allowed")
)

// ssoConfig is read from the environment on every exchange so a secret
// rotation only needs a container restart, and tests can drive it directly.
type ssoConfig struct {
	Enabled   bool
	Secret    string
	Issuer    string
	Audience  string
	ClockSkew time.Duration
}

// ssoReplayTTL bounds how long a consumed jti is remembered. It only has to
// outlive the token itself, plus clock skew.
const ssoReplayTTL = 10 * time.Minute

var (
	ssoSeenMu sync.Mutex
	ssoSeen   = map[string]int64{}
)

func loadSSOConfig() (*ssoConfig, error) {
	cfg := &ssoConfig{
		Enabled:   strings.EqualFold(strings.TrimSpace(os.Getenv("WEKNORA_SSO_ENABLED")), "true"),
		Secret:    os.Getenv("WEKNORA_SSO_SECRET"),
		Issuer:    strings.TrimSpace(os.Getenv("WEKNORA_SSO_ISSUER")),
		Audience:  strings.TrimSpace(os.Getenv("WEKNORA_SSO_AUDIENCE")),
		ClockSkew: 30 * time.Second,
	}
	if cfg.Audience == "" {
		cfg.Audience = "weknora"
	}
	if raw := strings.TrimSpace(os.Getenv("WEKNORA_SSO_CLOCK_SKEW_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 300 {
			cfg.ClockSkew = time.Duration(n) * time.Second
		}
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	if len(cfg.Secret) < 16 {
		return nil, fmt.Errorf("WEKNORA_SSO_SECRET must be at least 16 characters")
	}
	return cfg, nil
}

type ssoClaims struct {
	Subject  string
	Email    string
	Name     string
	TenantID uint64
	Role     types.TenantRole
	JTI      string
}

// parseSSOToken validates signature, algorithm, audience, issuer and expiry.
// It is intentionally a pure function of the token and config so it can be
// unit-tested without a database.
func parseSSOToken(raw string, cfg *ssoConfig) (*ssoClaims, error) {
	options := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(cfg.ClockSkew),
	}
	if cfg.Issuer != "" {
		options = append(options, jwt.WithIssuer(cfg.Issuer))
	}
	token, err := jwt.Parse(strings.TrimSpace(raw), func(*jwt.Token) (any, error) {
		return []byte(cfg.Secret), nil
	}, options...)
	if err != nil || token == nil || !token.Valid {
		return nil, ErrSSOInvalidToken
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrSSOInvalidToken
	}

	email := claimString(claims, "email")
	subject := claimString(claims, "sub")
	jti := claimString(claims, "jti")
	tenantID, err := claimUint64(claims, "tenant")
	if email == "" || subject == "" || jti == "" || err != nil || tenantID == 0 {
		return nil, ErrSSOInvalidToken
	}
	if exp, err := claims.GetExpirationTime(); err != nil || exp == nil {
		return nil, ErrSSOInvalidToken
	}
	issuedAt, err := claims.GetIssuedAt()
	if err != nil || issuedAt == nil {
		return nil, ErrSSOInvalidToken
	}
	expiresAt, _ := claims.GetExpirationTime()
	// Reject long-lived tokens outright: the exchange is only safe with a
	// token that a leaked log cannot replay hours later.
	if expiresAt.Time.Sub(issuedAt.Time) > 2*time.Minute {
		return nil, ErrSSOInvalidToken
	}

	role := types.TenantRole(strings.TrimSpace(claimString(claims, "role")))
	if role == "" {
		role = types.TenantRoleViewer
	}
	if !role.IsValid() || role == types.TenantRoleOwner {
		return nil, ErrSSORoleNotAllowed
	}

	return &ssoClaims{
		Subject:  subject,
		Email:    strings.ToLower(strings.TrimSpace(email)),
		Name:     claimString(claims, "name"),
		TenantID: tenantID,
		Role:     role,
		JTI:      jti,
	}, nil
}

func claimString(claims jwt.MapClaims, key string) string {
	if v, ok := claims[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func claimUint64(claims jwt.MapClaims, key string) (uint64, error) {
	switch v := claims[key].(type) {
	case float64:
		if v <= 0 || v != float64(uint64(v)) {
			return 0, fmt.Errorf("claim %s is not a positive integer", key)
		}
		return uint64(v), nil
	case string:
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil || n == 0 {
			return 0, fmt.Errorf("claim %s is not a positive integer", key)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("claim %s is missing", key)
	}
}

// consumeJTI reports false when the jti was already used. Entries are pruned
// lazily; a single instance only needs process-local memory. Multi-replica
// deployments should move this map to Redis.
func consumeJTI(jti string, now time.Time) bool {
	ssoSeenMu.Lock()
	defer ssoSeenMu.Unlock()
	cutoff := now.Add(-ssoReplayTTL).Unix()
	for key, expiry := range ssoSeen {
		if expiry < cutoff {
			delete(ssoSeen, key)
		}
	}
	if _, seen := ssoSeen[jti]; seen {
		return false
	}
	ssoSeen[jti] = now.Add(ssoReplayTTL).Unix()
	return true
}

// LoginWithSSO verifies a token signed by the SaaS platform, provisions the
// user on first sight, ensures workspace membership and returns the same
// response shape as a password login.
func (s *userService) LoginWithSSO(ctx context.Context, rawToken string) (*types.LoginResponse, error) {
	cfg, err := loadSSOConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrSSODisabled
	}
	claims, err := parseSSOToken(rawToken, cfg)
	if err != nil {
		return nil, err
	}
	if !consumeJTI(claims.JTI, time.Now()) {
		return nil, ErrSSOInvalidToken
	}

	tenant, err := s.tenantService.GetTenantByID(ctx, claims.TenantID)
	if err != nil || tenant == nil {
		return nil, ErrSSOTenantNotFound
	}

	user, err := s.userRepo.GetUserByEmail(ctx, claims.Email)
	if err != nil && !errors.Is(err, apprepo.ErrUserNotFound) {
		return nil, err
	}
	if user == nil {
		password, genErr := generateRandomString(32)
		if genErr != nil {
			return nil, genErr
		}
		username := s.generateOIDCUsername(ctx, &types.OIDCUserInfo{
			Username: claims.Name,
			Email:    claims.Email,
		})
		user, err = s.Register(ctx, &types.RegisterRequest{
			Username:           username,
			Email:              claims.Email,
			Password:           password,
			TenantProvisioning: types.TenantProvisioningTenantless,
		})
		if err != nil {
			return nil, err
		}
		logger.Infof(ctx, "[sso] provisioned user %s for workspace %d", user.ID, claims.TenantID)
	}
	if !user.IsActive {
		return nil, ErrSSOUserDisabled
	}

	if s.memberService != nil {
		if memberErr := s.ensureSSOMembership(ctx, user.ID, claims.TenantID, claims.Role); memberErr != nil {
			return nil, memberErr
		}
	}

	accessToken, refreshToken, err := s.generateTokensForTenant(ctx, user, claims.TenantID)
	if err != nil {
		return nil, err
	}

	if user.Preferences.LastActiveTenantID == nil || *user.Preferences.LastActiveTenantID != claims.TenantID {
		tenantID := claims.TenantID
		user.Preferences.LastActiveTenantID = &tenantID
		if updateErr := s.userRepo.UpdateUser(ctx, user); updateErr != nil {
			logger.Warnf(ctx, "[sso] failed to persist last active workspace for %s: %v", user.ID, updateErr)
		}
	}

	return &types.LoginResponse{
		Success:      true,
		Message:      "SSO login successful",
		User:         user,
		ActiveTenant: tenant,
		Memberships:  s.buildMembershipsForUser(ctx, user, tenant),
		Token:        accessToken,
		RefreshToken: refreshToken,
	}, nil
}

// ensureSSOMembership creates the membership on first sight and keeps the
// role in sync on every later exchange: the SaaS platform is the source of
// truth for roles. One deliberate exception — a workspace Owner is never
// demoted through SSO, so the local owner keeps control of the space (the
// token's role is ignored and a warning is logged).
func (s *userService) ensureSSOMembership(
	ctx context.Context, userID string, tenantID uint64, role types.TenantRole,
) error {
	membership, err := s.memberService.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if membership == nil {
		if _, addErr := s.memberService.AddMember(ctx, userID, tenantID, role, nil); addErr != nil {
			return addErr
		}
		logger.Infof(ctx, "[sso] added user %s to workspace %d as %s", userID, tenantID, role)
		return nil
	}
	if membership.Role == role {
		return nil
	}
	if membership.Role == types.TenantRoleOwner {
		logger.Warnf(ctx,
			"[sso] keeping workspace owner role for user %s in workspace %d (token role %s ignored)",
			userID, tenantID, role)
		return nil
	}
	if err := s.memberService.UpdateRole(ctx, userID, tenantID, role); err != nil {
		return err
	}
	logger.Infof(ctx, "[sso] synced role for user %s in workspace %d: %s -> %s",
		userID, tenantID, membership.Role, role)
	return nil
}
