package calling

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// isTerminalPeerState reports whether a peer connection has already died.
func isTerminalPeerState(pc *webrtc.PeerConnection) bool {
	state := pc.ConnectionState()
	return state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed
}

// recoverAndLog recovers from a panic in the calling goroutine, logs it with
// a stack trace, and — if a call session is associated — force-ends that
// session so its resources (PeerConnection, audio tracks, RTP readers,
// DB rows) don't leak and block a future redial from the same caller.
//
// An unrecovered panic in ANY goroutine crashes the entire Go process, not
// just the call that triggered it — every other active call and every call
// that would otherwise have come in seconds later goes down with it. This is
// the single highest-leverage fix for "IVR works sometimes then the service
// stops answering": one malformed SDP, one nil track, one out-of-range slice
// access on a single call was previously enough to take the whole server
// down until something external restarted the container.
//
// Usage: `defer m.recoverAndLog("someFunc", session.ID)` at the top of any
// function that runs in its own goroutine, or via safeGo below.
func (m *Manager) recoverAndLog(where, callID string) {
	if r := recover(); r != nil {
		m.log.Error("Recovered from panic in call goroutine — call ended, server kept running",
			"where", where,
			"call_id", callID,
			"panic", r,
			"stack", string(debug.Stack()),
		)
		if callID != "" {
			// EndCall is safe to call even if the session is already gone
			// or mid-cleanup (GetSession returns nil / cleanupSession is a
			// no-op on a missing session).
			m.EndCall(callID)
		}
	}
}

// safeGo runs fn in a new goroutine with panic recovery attached, so a bug
// triggered by one specific call can never crash the whole IVR/calling
// service. Prefer this over a bare `go m.someMethod(...)` for any call-
// handling goroutine.
func (m *Manager) safeGo(where, callID string, fn func()) {
	go func() {
		defer m.recoverAndLog(where, callID)
		fn()
	}()
}

// signalReject sends a WhatsApp "reject" action for a call that has not yet
// been pre-accepted/accepted. Uses its own short-lived context so it doesn't
// depend on the call's session context, which may already be canceled by
// the time we decide to reject (e.g. the peer connection died first).
func (m *Manager) signalReject(session *CallSession, waAccount *whatsapp.Account) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.rejectCall(ctx, waAccount, session.ID)
}

// signalTerminate sends a WhatsApp "terminate" action for a call that has
// already been pre-accepted/accepted. Thin naming-symmetric wrapper around
// terminateCall for use at abortIfPeerTerminal call sites in negotiateWebRTC.
func (m *Manager) signalTerminate(session *CallSession, waAccount *whatsapp.Account) {
	m.terminateCall(session, waAccount)
}

// StartWatchdog runs a background loop that force-cleans call sessions that
// have gotten stuck — negotiation that never reached a terminal state,
// a peer connection that silently died without an event ever landing on
// the manager (WhatsApp's own terminate/ended webhook can be dropped or
// delayed), or a call that has simply run past its configured maximum
// duration. Without this, a single stuck session sits in m.sessions forever;
// resetCallerSessions only clears it out on the caller's NEXT redial, so the
// first "call again" after a stuck call always looks like a failure/no
// answer to the caller even though the fix is already in place for the call
// after that.
//
// Call this once from main() after calling.NewManager(...), e.g.:
//
//	go callManager.StartWatchdog(context.Background())
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

// sweepStuckSessions scans all active sessions once and force-cleans any
// that are clearly dead or have exceeded sane time limits. Recovers its own
// panics so a bug here can never take down the watchdog loop itself.
func (m *Manager) sweepStuckSessions() {
	defer m.recoverAndLog("sweepStuckSessions", "")

	maxDuration := time.Duration(m.config.MaxCallDuration) * time.Second
	if maxDuration <= 0 {
		maxDuration = 1 * time.Hour
	}
	// A call that hasn't reached "answered" within this long was never
	// going to connect — WhatsApp's own call setup timeout is well under
	// this, so anything still "ringing"/negotiating this long is orphaned
	// state, not a slow-but-live call.
	const maxNegotiationAge = 90 * time.Second
	// Hard ceiling regardless of status — belt and braces against any
	// status value we didn't account for above.
	const absoluteMaxAge = 6 * time.Hour

	now := time.Now()

	m.mu.RLock()
	var stuck []*CallSession
	for _, session := range m.sessions {
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
			// Session's own context was canceled (call ended/reset/timed
			// out) but cleanupSession never actually removed it from the
			// map — e.g. a goroutine crashed between Cancel() and cleanup,
			// or cleanup returned early on the "transfer waiting" guard and
			// nothing ever re-triggered it.
			stuck = append(stuck, session)
		case age > absoluteMaxAge:
			stuck = append(stuck, session)
		case status != models.CallStatusAnswered && age > maxNegotiationAge:
			stuck = append(stuck, session)
		case peer != nil && isTerminalPeerState(peer) && age > 5*time.Second:
			stuck = append(stuck, session)
		}
	}
	m.mu.RUnlock()

	for _, session := range stuck {
		m.log.Error("Watchdog force-cleaning stuck call session",
			"call_id", session.ID,
			"caller", session.CallerPhone,
			"age_secs", int(now.Sub(session.StartedAt).Seconds()),
		)
		// Best-effort tell WhatsApp the call is over so a stuck local
		// session can't also leave Meta's side thinking the call is live.
		go m.terminateCallBySession(session)
		m.EndCall(session.ID)
	}
}
