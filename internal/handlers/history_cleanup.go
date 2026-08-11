package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// ClearCallHistory soft-deletes only terminal call log records visible to the
// authenticated user. Live/ringing/transferring calls are intentionally never
// touched, and soft deletion preserves relational integrity for transfers and
// recordings.
func (a *App) ClearCallHistory(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	terminal := []models.CallStatus{
		models.CallStatusCompleted,
		models.CallStatusMissed,
		models.CallStatusRejected,
		models.CallStatusFailed,
	}

	query := a.DB.Where("organization_id = ? AND status IN ?", orgID, terminal)
	// Keep the same visibility contract as ListCallLogs: users with the global
	// call_logs:read permission may clear the org history they can see; agents
	// without it may clear only their own terminal calls.
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("agent_id = ?", userID)
	}

	res := query.Delete(&models.CallLog{})
	if res.Error != nil {
		a.Log.Error("Failed to clear call history", "error", res.Error, "org_id", orgID, "user_id", userID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to clear call history", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"status":  "cleared",
		"deleted": res.RowsAffected,
	})
}

// DeleteConversation permanently removes the conversation and resets the
// contact to a clean state. Contact identity, tags, metadata, call permissions,
// and call history remain because they are not part of the chat conversation.
func (a *App) DeleteConversation(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	var counts conversationDeleteCounts
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		var deleteErr error
		counts, deleteErr = hardDeleteConversationData(tx, orgID, contactID, false)
		if deleteErr != nil {
			return deleteErr
		}
		return tx.Model(&models.Contact{}).
			Where("id = ? AND organization_id = ?", contactID, orgID).
			Updates(map[string]any{
				"last_message_at":         nil,
				"last_message_preview":    "",
				"last_inbound_at":         nil,
				"is_read":                 true,
				"assigned_user_id":        nil,
				"chatbot_last_message_at": nil,
				"chatbot_reminder_sent":   false,
			}).Error
	}); err != nil {
		a.Log.Error("Failed to permanently delete conversation", "error", err, "org_id", orgID, "contact_id", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to permanently delete conversation", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"status":                   "permanently_deleted",
		"deleted_messages":         counts.Messages,
		"deleted_notes":            counts.Notes,
		"deleted_chatbot_sessions": counts.ChatbotSessions,
		"deleted_agent_transfers":  counts.AgentTransfers,
		"contact_id":               contactID.String(),
	})
}
