package models

import (
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BeforeUpdate keeps IVR flow account assignments in sync when an account is
// renamed. IVR flows intentionally store the human-readable account name, so
// changing WhatsAppAccount.Name must be cascaded in the same DB transaction.
//
// Guarding on ID/Name keeps bulk updates (for example default-account flag
// resets) from triggering an unnecessary lookup/cascade.
func (a *WhatsAppAccount) BeforeUpdate(tx *gorm.DB) error {
	if a == nil || a.ID == uuid.Nil || strings.TrimSpace(a.Name) == "" {
		return nil
	}

	var previous struct {
		Name string
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Model(&WhatsAppAccount{}).
		Select("name").
		Where("id = ? AND organization_id = ?", a.ID, a.OrganizationID).
		Take(&previous).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}

	oldName := strings.TrimSpace(previous.Name)
	newName := strings.TrimSpace(a.Name)
	if oldName == "" || oldName == newName {
		return nil
	}

	return tx.Model(&IVRFlow{}).
		Where("organization_id = ? AND whatsapp_account = ?", a.OrganizationID, oldName).
		Update("whatsapp_account", newName).Error
}
