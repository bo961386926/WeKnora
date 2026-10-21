package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/logger"
)

type ssoExchangeRequest struct {
	Token string `json:"token" binding:"required"`
}

// SSOExchange exchanges a short-lived token signed by the SaaS platform for a
// WeKnora login. The endpoint is public but rate-limited, and the token must
// carry a single-use jti.
func (h *AuthHandler) SSOExchange(c *gin.Context) {
	var req ssoExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "token is required"})
		return
	}

	resp, err := h.userService.LoginWithSSO(c.Request.Context(), req.Token)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSSODisabled):
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "SSO is not enabled"})
		case errors.Is(err, service.ErrSSOTenantNotFound):
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Workspace not found"})
		case errors.Is(err, service.ErrSSOUserDisabled):
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "User is disabled"})
		case errors.Is(err, service.ErrSSORoleNotAllowed):
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Role not allowed"})
		default:
			logger.Warnf(c.Request.Context(), "[sso] exchange rejected: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid or expired SSO token"})
		}
		return
	}

	c.JSON(http.StatusOK, dto.NewAuthLoginResponse(resp))
}
