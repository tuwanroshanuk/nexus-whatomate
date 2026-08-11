package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

type conversationDeleteCounts struct {
	Messages         int64
	Notes            int64
	ChatbotSessions  int64
	AgentTransfers   int64
	CallLogs         int64
	CallTransfers    int64
	CallPermissions  int64
}

// hardDeleteConversationData permanently removes every record that represents
// conversation state for one contact. The caller owns the transaction so a
// failure can never leave a partially-cleaned contact behind.
func hardDeleteConversationData(tx *gorm.DB, orgID, contactID uuid.UUID, includeCalls bool) (conversationDeleteCounts, error) {
	var counts conversationDeleteCounts

	messageIDs := tx.Unscoped().Model(&models.Message{}).
		Select("id").Where("organization_id = ? AND contact_id = ?", orgID, contactID)
	if err := tx.Unscoped().Model(&models.BulkMessageRecipient{}).
		Where("message_id IN (?)", messageIDs).Update("message_id", nil).Error; err != nil {
		return counts, err
	}
	if err := tx.Unscoped().Model(&models.Message{}).
		Where("organization_id = ? AND contact_id = ?", orgID, contactID).
		Update("reply_to_message_id", nil).Error; err != nil {
		return counts, err
	}
	result := tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.Message{})
	if result.Error != nil {
		return counts, result.Error
	}
	counts.Messages = result.RowsAffected

	sessionIDs := tx.Unscoped().Model(&models.ChatbotSession{}).
		Select("id").Where("organization_id = ? AND contact_id = ?", orgID, contactID)
	if err := tx.Unscoped().Where("session_id IN (?)", sessionIDs).Delete(&models.ChatbotSessionMessage{}).Error; err != nil {
		return counts, err
	}
	result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.ChatbotSession{})
	if result.Error != nil {
		return counts, result.Error
	}
	counts.ChatbotSessions = result.RowsAffected

	result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.AgentTransfer{})
	if result.Error != nil {
		return counts, result.Error
	}
	counts.AgentTransfers = result.RowsAffected

	result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.ConversationNote{})
	if result.Error != nil {
		return counts, result.Error
	}
	counts.Notes = result.RowsAffected

	if includeCalls {
		result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.CallTransfer{})
		if result.Error != nil {
			return counts, result.Error
		}
		counts.CallTransfers = result.RowsAffected

		result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.CallPermission{})
		if result.Error != nil {
			return counts, result.Error
		}
		counts.CallPermissions = result.RowsAffected

		result = tx.Unscoped().Where("organization_id = ? AND contact_id = ?", orgID, contactID).Delete(&models.CallLog{})
		if result.Error != nil {
			return counts, result.Error
		}
		counts.CallLogs = result.RowsAffected
	}

	return counts, nil
}
