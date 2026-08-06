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
