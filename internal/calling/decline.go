package calling

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
)

// DeclineTransfer removes one agent from the current transfer attempt without
// ending the caller's WhatsApp call. For team transfers it immediately starts
// a new rotation with every previously tried/declining agent excluded. If no
// eligible agents remain, the normal transfer no-answer path is used so the
// IVR can continue along its configured timeout/no_answer edge.
func (m *Manager) DeclineTransfer(transferID, agentID uuid.UUID) error {
	session := m.findSessionByTransferID(transferID)
	if session == nil {
		return fmt.Errorf("no active session for transfer %s", transferID)
	}

	var transfer models.CallTransfer
	if err := m.db.Where("id = ?", transferID).First(&transfer).Error; err != nil {
		return fmt.Errorf("transfer not found: %w", err)
	}
	if transfer.Status != models.CallTransferStatusWaiting {
		return fmt.Errorf("transfer is no longer waiting")
	}

	session.mu.Lock()
	if session.TransferStatus != models.CallTransferStatusWaiting {
		session.mu.Unlock()
		return fmt.Errorf("transfer is no longer waiting")
	}

	// When rotation has assigned a specific current agent, only that agent may
	// decline this attempt. During the final broadcast AgentID is nil and every
	// notified team member may independently decline.
	if transfer.AgentID != nil && *transfer.AgentID != agentID {
		session.mu.Unlock()
		return fmt.Errorf("transfer is currently assigned to another agent")
	}

	oldAccepted := session.TransferAccepted
	// Every rotation generation gets its own signal. Closing the previous
	// generation makes the old rotation goroutine exit through its accepted
	// branch, which is intentionally side-effect free; the fresh generation
	// below then owns routing.
	if oldAccepted != nil {
		safeClose(oldAccepted)
	}
	session.TransferAccepted = make(chan struct{})
	session.mu.Unlock()

	tried := triedAgentUUIDs(transfer.TriedAgentIDs)
	if !containsAgent(tried, agentID) {
		tried = append(tried, agentID)
	}
	triedJSON := make(models.JSONBArray, len(tried))
	for i, id := range tried {
		triedJSON[i] = id.String()
	}

	if err := m.db.Model(&models.CallTransfer{}).
		Where("id = ? AND status = ?", transferID, models.CallTransferStatusWaiting).
		Updates(map[string]any{
			"agent_id":        nil,
			"tried_agent_ids": triedJSON,
		}).Error; err != nil {
		return fmt.Errorf("failed to record transfer decline: %w", err)
	}

	m.wsHub.BroadcastToUser(session.OrganizationID, agentID, websocket.WSMessage{
		Type: websocket.TypeCallTransferReassigned,
		Payload: map[string]any{
			"id":     transferID.String(),
			"reason": "declined",
		},
	})

	m.log.Info("Agent declined call transfer; rerouting",
		"transfer_id", transferID,
		"agent_id", agentID,
		"tried_agents", len(tried),
	)

	if transfer.TeamID == nil {
		// There is no next team member. Resume the IVR's configured no-answer
		// edge (or terminate according to the existing transfer policy).
		m.handleTransferNoAnswer(session, transferID)
		return nil
	}

	transfer.AgentID = nil
	transfer.TriedAgentIDs = triedJSON
	orgSettings := m.getOrgCallingSettings(session.OrganizationID)
	go m.runDeclinedTransferRotation(session, transfer, orgSettings, tried)
	return nil
}

// runDeclinedTransferRotation is the continuation used after an explicit
// decline. The original rotation goroutine has already been signalled to exit;
// this continuation preserves the tried-agent set so a declining device can
// never be selected again for the same transfer.
func (m *Manager) runDeclinedTransferRotation(
	session *CallSession,
	transfer models.CallTransfer,
	orgSettings orgCallingSettings,
	triedAgents []uuid.UUID,
) {
	defer m.recoverAndLog("runDeclinedTransferRotation", session.ID)
	if transfer.TeamID == nil {
		m.handleTransferNoAnswer(session, transfer.ID)
		return
	}

	teamID := *transfer.TeamID
	orgID := transfer.OrganizationID

	teamCfg := m.assigner.GetTeamConfig(teamID)
	teamTimeout := 0
	if teamCfg != nil {
		teamTimeout = teamCfg.PerAgentTimeoutSecs
	}
	perAgentSecs := assignment.ResolvePerAgentTimeout(teamTimeout, 0, m.config.PerAgentTimeoutSecs)
	perAgentTimeout := time.Duration(perAgentSecs) * time.Second

	// Preserve the original transfer's total timeout instead of granting a new
	// full timeout after every decline. This keeps IVR timing deterministic.
	totalSecs := orgSettings.TransferTimeoutSecs
	if totalSecs <= 0 {
		totalSecs = 30
	}
	deadline := transfer.TransferredAt.Add(time.Duration(totalSecs) * time.Second)
	if deadline.Before(time.Now()) {
		m.handleTransferNoAnswer(session, transfer.ID)
		return
	}
	totalCtx, totalCancel := context.WithDeadline(context.Background(), deadline)
	defer totalCancel()

	session.mu.Lock()
	session.TransferCancel = totalCancel
	session.mu.Unlock()

	basePayload := map[string]any{
		"id":               transfer.ID.String(),
		"call_log_id":      transfer.CallLogID.String(),
		"whatsapp_call_id": transfer.WhatsAppCallID,
		"caller_phone":     m.maybeMaskPhone(transfer.OrganizationID, transfer.CallerPhone),
		"contact_id":       transfer.ContactID.String(),
		"whatsapp_account": transfer.WhatsAppAccount,
		"team_id":          teamID.String(),
		"transferred_at":   transfer.TransferredAt.Format(time.RFC3339),
	}
	if transfer.InitiatingAgentID != nil {
		basePayload["initiating_agent_id"] = transfer.InitiatingAgentID.String()
	}

	for totalCtx.Err() == nil {
		session.mu.Lock()
		status := session.TransferStatus
		session.mu.Unlock()
		if status != models.CallTransferStatusWaiting {
			return
		}

		agentID := m.assigner.AssignToTeam(teamID, orgID, triedAgents, assignment.CallLoadCounter)
		if agentID == nil {
			break
		}
		triedAgents = append(triedAgents, *agentID)
		if !m.wsHub.IsUserOnline(orgID, *agentID) {
			continue
		}

		triedIDs := make(models.JSONBArray, len(triedAgents))
		for i, id := range triedAgents {
			triedIDs[i] = id.String()
		}
		m.db.Model(&models.CallTransfer{}).Where("id = ?", transfer.ID).Updates(map[string]any{
			"agent_id":        agentID,
			"tried_agent_ids": triedIDs,
		})

		agentPayload := make(map[string]any)
		maps.Copy(agentPayload, basePayload)
		agentPayload["assigned_to_you"] = true
		agentPayload["agent_id"] = agentID.String()
		m.wsHub.BroadcastToUser(orgID, *agentID, websocket.WSMessage{
			Type:    websocket.TypeCallTransferWaiting,
			Payload: agentPayload,
		})

		m.log.Info("Decline rotation assigned transfer to next agent",
			"transfer_id", transfer.ID,
			"agent_id", *agentID,
			"attempt", len(triedAgents),
		)

		agentTimer := time.NewTimer(perAgentTimeout)
		session.mu.Lock()
		accepted := session.TransferAccepted
		session.mu.Unlock()

		select {
		case <-agentTimer.C:
			m.wsHub.BroadcastToUser(orgID, *agentID, websocket.WSMessage{
				Type: websocket.TypeCallTransferReassigned,
				Payload: map[string]any{
					"id":     transfer.ID.String(),
					"reason": "timeout",
				},
			})
			m.db.Model(&models.CallTransfer{}).Where("id = ?", transfer.ID).Update("agent_id", nil)
			continue

		case <-accepted:
			// Either the call was accepted or DeclineTransfer replaced this
			// generation and started a newer rotation. In both cases this
			// goroutine no longer owns routing.
			agentTimer.Stop()
			return

		case <-totalCtx.Done():
			agentTimer.Stop()
			m.wsHub.BroadcastToUser(orgID, *agentID, websocket.WSMessage{
				Type: websocket.TypeCallTransferReassigned,
				Payload: map[string]any{
					"id":     transfer.ID.String(),
					"reason": "total_timeout",
				},
			})
			m.db.Model(&models.CallTransfer{}).Where("id = ?", transfer.ID).Update("agent_id", nil)
			m.handleTransferNoAnswer(session, transfer.ID)
			return
		}
	}

	session.mu.Lock()
	status := session.TransferStatus
	accepted := session.TransferAccepted
	session.mu.Unlock()
	if status != models.CallTransferStatusWaiting {
		return
	}

	remaining := m.assigner.GetAvailableAgents(teamID, triedAgents)
	remaining = m.wsHub.FilterOnlineUsers(orgID, remaining)
	if len(remaining) == 0 {
		m.handleTransferNoAnswer(session, transfer.ID)
		return
	}

	remainingSet := make([]any, len(remaining))
	for i, id := range remaining {
		remainingSet[i] = id.String()
	}
	m.db.Model(&models.CallTransfer{}).Where("id = ?", transfer.ID).Updates(map[string]any{
		"agent_id": nil,
	})

	fallbackPayload := make(map[string]any)
	maps.Copy(fallbackPayload, basePayload)
	fallbackPayload["fallback_broadcast"] = true
	fallbackPayload["eligible_agent_ids"] = remainingSet
	m.wsHub.BroadcastToUsers(orgID, remaining, websocket.WSMessage{
		Type:    websocket.TypeCallTransferWaiting,
		Payload: fallbackPayload,
	})

	select {
	case <-accepted:
		return
	case <-totalCtx.Done():
		m.wsHub.BroadcastToUsers(orgID, remaining, websocket.WSMessage{
			Type: websocket.TypeCallTransferReassigned,
			Payload: map[string]any{
				"id":     transfer.ID.String(),
				"reason": "total_timeout",
			},
		})
		m.handleTransferNoAnswer(session, transfer.ID)
		return
	}
}

func triedAgentUUIDs(values models.JSONBArray) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(values))
	for _, raw := range values {
		id, err := uuid.Parse(fmt.Sprint(raw))
		if err == nil && !containsAgent(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func containsAgent(values []uuid.UUID, id uuid.UUID) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}
