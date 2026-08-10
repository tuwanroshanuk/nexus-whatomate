package calling

import (
	"sync"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
)

type sessionTermination struct {
	once sync.Once
}

var sessionTerminations sync.Map // map[*CallSession]*sessionTermination

func (m *Manager) isCurrentSession(session *CallSession) bool {
	if session == nil {
		return false
	}
	m.mu.RLock()
	current, exists := m.sessions[session.ID]
	m.mu.RUnlock()
	return exists && current == session
}

func (m *Manager) endSession(session *CallSession, reason, initiator string) {
	if session == nil {
		return
	}

	value, _ := sessionTerminations.LoadOrStore(session, &sessionTermination{})
	termination := value.(*sessionTermination)
	termination.once.Do(func() {
		if !m.isCurrentSession(session) {
			m.log.Info("Ignoring stale call cleanup",
				"call_id", session.ID,
				"reason", reason,
				"initiator", initiator,
			)
			sessionTerminations.Delete(session)
			return
		}

		session.mu.Lock()
		startedAt := session.StartedAt
		direction := session.Direction
		if session.Cancel != nil {
			session.Cancel()
		}
		session.Status = models.CallStatusCompleted
		session.mu.Unlock()

		m.log.Info("Ending call session",
			"call_id", session.ID,
			"reason", reason,
			"initiator", initiator,
			"age_ms", time.Since(startedAt).Milliseconds(),
		)

		// Agent/Flutter process loss reaches the legacy EndCall path from the
		// outgoing agent PeerConnection callback. Generic WebRTC cleanup alone
		// only closes local peer resources; it does not guarantee that Meta's
		// WhatsApp call leg receives a terminate command. Force termination here
		// for that specific outgoing-disconnect path so swiping/killing the app
		// cannot leave the remote party in an orphaned call.
		//
		// Explicit user Hang Up does NOT come through this branch; it continues
		// to use HangupOutgoingCall, preserving configured outgoing_end IVR.
		if direction == models.CallDirectionOutgoing && initiator == "EndCall" {
			m.terminateCallBySession(session)
		}

		m.cleanupSession(session.ID)
		sessionTerminations.Delete(session)
	})
}

func (m *Manager) endCurrentSessionByID(callID, reason, initiator string) {
	if callID == "" {
		return
	}
	if session := m.GetSession(callID); session != nil {
		m.endSession(session, reason, initiator)
	}
}
