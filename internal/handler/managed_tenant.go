package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ManagedTenantHandler serves the SaaS-facing managed tenant API: idempotent
// workspace provisioning keyed by a stable external reference, plus member
// removal. Authenticated with a platform API key carrying
// system_tenants_manage; system administrators may also call it directly.
type ManagedTenantHandler struct {
	managedSvc interfaces.ManagedTenantService
}

// NewManagedTenantHandler wires the handler.
func NewManagedTenantHandler(managedSvc interfaces.ManagedTenantService) *ManagedTenantHandler {
	return &ManagedTenantHandler{managedSvc: managedSvc}
}

type upsertManagedTenantRequest struct {
	// Name of the workspace. Required when creating; when present on an
	// update the workspace is renamed.
	Name string `json:"name"`
	// Active gates SSO and member sync for the bound workspace. Defaults
	// to true.
	Active *bool `json:"active"`
}

// UpsertManagedTenant handles PUT /system/admin/managed-tenants/:external_ref.
func (h *ManagedTenantHandler) UpsertManagedTenant(c *gin.Context) {
	var req upsertManagedTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	active := req.Active == nil || *req.Active

	tenant, created, err := h.managedSvc.ProvisionManagedTenant(
		c.Request.Context(), types.DefaultTenantBindingSource,
		c.Param("external_ref"), req.Name, active)
	if err != nil {
		h.mapError(c, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{
		"success": true,
		"data": gin.H{
			"tenant_id": tenant.ID,
			"name":      tenant.Name,
			"status":    tenant.Status,
			"created":   created,
		},
	})
}

// RemoveManagedTenantMember handles
// DELETE /system/admin/managed-tenants/:external_ref/members/:external_user.
// The external user reference is the account email.
func (h *ManagedTenantHandler) RemoveManagedTenantMember(c *gin.Context) {
	if err := h.managedSvc.RemoveMemberByEmail(
		c.Request.Context(), types.DefaultTenantBindingSource,
		c.Param("external_ref"), c.Param("external_user"),
	); err != nil {
		h.mapError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *ManagedTenantHandler) mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrManagedTenantInvalid):
		c.Error(apperrors.NewValidationError(err.Error()))
	case errors.Is(err, service.ErrManagedTenantNotFound), errors.Is(err, service.ErrManagedUserNotFound),
		errors.Is(err, service.ErrManagedUserNotMember):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": err.Error()})
	case errors.Is(err, service.ErrManagedTenantUnusable):
		logger.Errorf(c.Request.Context(), "[managed] dangling binding: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "workspace binding is dangling"})
	default:
		logger.Warnf(c.Request.Context(), "[managed] request failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "internal error"})
	}
}
