package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tenantBindingRepository is the gorm implementation of
// interfaces.TenantBindingRepository. Lookups follow gorm's soft-delete
// semantics: a deleted binding reads as absent.
type tenantBindingRepository struct {
	db *gorm.DB
}

// NewTenantBindingRepository creates a new tenant binding repository.
func NewTenantBindingRepository(db *gorm.DB) interfaces.TenantBindingRepository {
	return &tenantBindingRepository{db: db}
}

func (r *tenantBindingRepository) GetByExternalRef(
	ctx context.Context, source, externalID string,
) (*types.TenantBinding, error) {
	var binding types.TenantBinding
	err := r.db.WithContext(ctx).
		Where("external_source = ? AND external_tenant_id = ?", source, externalID).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func (r *tenantBindingRepository) GetByTenantID(
	ctx context.Context, tenantID uint64,
) (*types.TenantBinding, error) {
	var binding types.TenantBinding
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func (r *tenantBindingRepository) Create(ctx context.Context, binding *types.TenantBinding) error {
	return r.db.WithContext(ctx).Create(binding).Error
}

func (r *tenantBindingRepository) Update(ctx context.Context, binding *types.TenantBinding) error {
	return r.db.WithContext(ctx).Save(binding).Error
}
