package calling

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

func isTerminalPeerState(pc *webrtc.PeerConnection) bool {
	state := pc.ConnectionState()
	if state == webrtc.PeerConnectionStateClosed {
		return true
	}
	if state == webrtc.PeerConnectionStateFailed {
		// PeerConnectionState=failed with ICE still active is a transient DTLS
		// race — Meta's media server fires ClientHello after AcceptCall completes
		// (~0.8s), and Pion marks the DTLS transport failed if the first
		// handshake times out. ICE being connected means the underlying path is
		// still alive and DTLS recovery is possible. Do NOT consider this
		// terminal; the negotiateWebRTC media-wait loop handles the actual
		// timeout (30s) and will tear down correctly if DTLS never recovers.
		iceState := pc.ICEConnectionState()
		if iceState == webrtc.ICEConnectionStateConnected ||
			iceState == webrtc.ICEConnectionStateChecking ||
			iceState == webrtc.ICEConnectionStateCompleted {
			return false // ICE alive — DTLS may still recover
		}
		return true
	}
	return false
}

func (m *Manager) recoverAndLog(where, callID string) {
	if r := recover(); r != nil {
		m.log.Error("Recovered from panic in call goroutine — call ended, server kept running",
			"where", where,
			"call_id", callID,
			"panic", r,
			"stack", string(debug.Stack()),
		)
		m.endCurrentSessionByID(callID, "panic_recovered", where)
	}
}

func (m *Manager) safeGo(where, callID string, fn func()) {
	go func() {
		defer m.recoverAndLog(where, callID)
		fn()
	}()
}

func (m *Manager) signalReject(session *CallSession, waAccount *whatsapp.Account) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.rejectCall(ctx, waAccount, session.ID)
}

func (m *Manager) signalTerminate(session *CallSession, waAccount *whatsapp.Account) {
	m.terminateCall(session, waAccount)
}

func (m *Manager) StartWatchdog(ctx context.Context) {
	const checkInterval = 15 * time.Second
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	m.log.Info("Call session watchdog started", "interval", checkInterval.String())

	for {
		select {
		case <-ctx.Done():
			m.log.Info("Call session watchdog stopped")
			return
		case <-ticker.C:
			m.sweepStuckSessions()
		}
	}
}

func (m *Manager) sweepStuckSessions() {
	defer m.recoverAndLog("sweepStuckSessions", "")

	maxDuration := time.Duration(m.config.MaxCallDuration) * time.Second
	if maxDuration <= 0 {
		maxDuration = time.Hour
	}
	const maxNegotiationAge = 90 * time.Second
	const absoluteMaxAge = 6 * time.Hour
	now := time.Now()

	// Snapshot manager state first, then inspect sessions after releasing
	// m.mu. This removes the manager/session lock-order deadlock.
	m.mu.RLock()
	sessions := make([]*CallSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	var stuck []*CallSession
	for _, session := range sessions {
		session.mu.Lock()
		age := now.Sub(session.StartedAt)
		status := session.Status
		peer := session.PeerConnection
		ctxDone := false
		if session.Context != nil {
			select {
			case <-session.Context.Done():
				ctxDone = true
			default:
			}
		}
		session.mu.Unlock()

		switch {
		case ctxDone && age > 5*time.Second:
			stuck = append(stuck, session)
		case age > absoluteMaxAge:
			stuck = append(stuck, session)
		case status != models.CallStatusAnswered && age > maxNegotiationAge:
			stuck = append(stuck, session)
		case peer != nil && isTerminalPeerState(peer) && age > 5*time.Second:
			stuck = append(stuck, session)
		}
	}

	for _, session := range stuck {
		if !m.isCurrentSession(session) {
			continue
		}
		m.log.Error("Watchdog force-cleaning stuck call session",
			"call_id", session.ID,
			"caller", session.CallerPhone,
			"age_secs", int(now.Sub(session.StartedAt).Seconds()),
		)
		m.safeGo("watchdogTerminate", session.ID, func() {
			m.terminateCallBySession(session)
		})
		m.endSession(session, "watchdog_stuck_session", "sweepStuckSessions")
	}
}
