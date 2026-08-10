package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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

// DeleteConversation clears the message history for one contact without
// deleting the contact or any policy/calling state attached to it. In
// particular LastInboundAt is deliberately retained because WhatsApp's 24-hour
// customer-service window must remain accurate after a user clears a chat.
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

	// Messages embed BaseModel, so this is a soft delete. Reply/reaction foreign
	// keys and audit/history references therefore remain structurally valid.
	res := a.DB.Where("organization_id = ? AND contact_id = ?", orgID, contactID).
		Delete(&models.Message{})
	if res.Error != nil {
		a.Log.Error("Failed to delete conversation", "error", res.Error, "org_id", orgID, "contact_id", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete conversation", nil, "")
	}

	// Reset only the conversation-list presentation fields. Do not clear
	// LastInboundAt, assignment, tags, metadata, call permissions, notes, or
	// call history.
	if err := a.DB.Model(&models.Contact{}).
		Where("id = ? AND organization_id = ?", contactID, orgID).
		Updates(map[string]any{
			"last_message_at":      nil,
			"last_message_preview": "",
			"is_read":              true,
		}).Error; err != nil {
		a.Log.Error("Failed to reset conversation summary", "error", err, "contact_id", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Conversation messages were cleared but the summary could not be reset", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"status":          "deleted",
		"deleted_messages": res.RowsAffected,
		"contact_id":      contactID.String(),
		"last_inbound_at": contact.LastInboundAt,
	})
}
