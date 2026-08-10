package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// DeclineCallTransfer declines only the current agent's transfer attempt.
// It never hangs up the caller. Team transfers are immediately rerouted by the
// calling manager; if no eligible agent remains the existing IVR no-answer
// branch resumes.
func (a *App) DeclineCallTransfer(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceCallTransfers, models.ActionWrite)
	if err != nil {
		return nil
	}

	transferID, err := parsePathUUID(r, "id", "call transfer")
	if err != nil {
		return nil
	}

	var transfer models.CallTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).
		First(&transfer).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Call transfer not found", nil, "")
	}
	if transfer.Status != models.CallTransferStatusWaiting {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Transfer is no longer waiting", nil, "")
	}

	// A specifically assigned ring may only be declined by that agent. During
	// the final team broadcast AgentID is nil, so any eligible team member can
	// decline their own ringing surface without affecting the others.
	if transfer.AgentID != nil && *transfer.AgentID != userID {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Transfer is currently assigned to another agent", nil, "")
	}

	if transfer.TeamID != nil && !a.IsSuperAdmin(userID) {
		var memberCount int64
		a.DB.Table("team_members").
			Where("team_id = ? AND user_id = ? AND deleted_at IS NULL", transfer.TeamID, userID).
			Count(&memberCount)
		if memberCount == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You are not a member of the target team", nil, "")
		}
	}

	if a.CallManager == nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Calling is not enabled", nil, "")
	}
	if err := a.CallManager.DeclineTransfer(transferID, userID); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, err.Error(), nil, "")
	}

	return r.SendEnvelope(map[string]string{
		"status": "declined",
		"routing": "next_available_agent",
	})
}
