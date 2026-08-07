package calling

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// negotiateWebRTC handles the SDP exchange and sets up WebRTC media.
//
// Per the WhatsApp Business Calling API (user-initiated calls):
//  1. Webhook "connect" delivers the consumer's SDP offer (in session.sdp)
//  2. Business creates a PeerConnection and sets the offer as remote description
//  3. Business creates an SDP answer
//  4. Business sends pre_accept with session: { sdp_type: "answer", sdp: "<SDP>" }
//  5. Business sends accept with the same session object
//  6. WebRTC media flows
//
// callerPhone is the E.164 phone number of the caller, used to apply a
// per-caller ICE cooldown on rapid redial (see callerCooldowns in session.go).
func (m *Manager) negotiateWebRTC(session *CallSession, account *models.WhatsAppAccount, sdpOffer string, callerPhone string) {
	// A panic anywhere below (a bad SDP, an unexpected nil, a pion internal
	// edge case) must end this one call, not the whole process — every other
	// active call and every future incoming call has to keep working.
	defer m.recoverAndLog("negotiateWebRTC", session.ID)

	// Per-caller ICE cooldown: if this caller ended a session very recently
	// (rapid redial), Meta's TURN relay may still hold the old allocation.
	// A new DTLS handshake on the same allocation arrives as a conflict and
	// Meta's media server sends a fatal DTLS alert — Pion records
	// PeerConnectionState=failed within ~100ms of the accept, with the ICE
	// pair already selected (host or relay). Sleeping the remainder of a 350ms
	// guard window gives Meta's infrastructure time to fully release the
	// old allocation before we begin ICE gathering on the new call.
	// This mirrors the channel-release guard timer used in GSM (T3105/T3120).
	const callerCooldownWindow = 350 * time.Millisecond
	if callerPhone != "" {
		m.cooldownMu.Lock()
		if last, ok := m.callerCooldowns[callerPhone]; ok {
			elapsed := time.Since(last)
			if elapsed < callerCooldownWindow {
				remaining := callerCooldownWindow - elapsed
				m.cooldownMu.Unlock()
				m.log.Info("Applying per-caller ICE cooldown before negotiation",
					"call_id", session.ID,
					"caller", callerPhone,
					"sleep_ms", remaining.Milliseconds(),
				)
				time.Sleep(remaining)
			} else {
				m.cooldownMu.Unlock()
			}
		} else {
			m.cooldownMu.Unlock()
		}
	}

	// Set up the session context. The done-check here catches two cases:
	// 1. The caller hung up before the negotiateWebRTC goroutine started.
	// 2. The caller hung up during the cooldown sleep above.
	// Either way, bail before allocating any WebRTC resources.
	baseCtx := session.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	select {
	case <-baseCtx.Done():
		return
	default:
	}

	ctx, cancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer cancel()

	waAccount := account.ToWAAccount()

	// Create peer connection with Opus codec
	pc, err := m.createPeerConnection()
	if err != nil {
		m.log.Error("Failed to create peer connection", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	session.mu.Lock()
	session.PeerConnection = pc
	session.mu.Unlock()

	// Add local audio track for IVR playback / server→caller audio
	audioTrack, err := createOpusTrack(pc, "ivr-audio")
	if err != nil {
		m.log.Error("Failed to create audio track", "error", err)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	session.mu.Lock()
	session.AudioTrack = audioTrack
	session.mu.Unlock()

	// Register handler for incoming audio (caller's voice + DTMF)
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		m.log.Info("Received remote track",
			"call_id", session.ID,
			"codec", track.Codec().MimeType,
			"payload_type", track.PayloadType(),
		)

		// Check if this is a dedicated telephone-event track (DTMF)
		if track.Codec().MimeType == "audio/telephone-event" {
			m.safeGo("handleDTMFTrack", session.ID, func() {
				m.handleDTMFTrack(session, track)
			})
			return
		}

		// Store the caller's remote track for potential audio bridge use
		session.mu.Lock()
		session.CallerRemoteTrack = track
		bridge := session.Bridge
		agentLocal := session.AgentAudioTrack
		session.mu.Unlock()

		// On incoming calls the caller's media often starts only after the agent
		// answers, so the transfer bridge may already be running without the
		// caller track (one-way audio). Wire the track into the live bridge now
		// so the agent can hear the caller; the bridge becomes the sole reader.
		if bridge != nil && agentLocal != nil {
			bridge.AttachCaller(track, agentLocal)
			return
		}

		// Consume audio and detect inline DTMF (telephone-event packets
		// arrive on the same m-line as audio with a different payload type).
		m.safeGo("consumeAudioWithDTMF", session.ID, func() {
			m.consumeAudioWithDTMF(session, track)
		})
	})

	// Buffered state events let negotiation observe terminal transitions without
	// blocking Pion's callback goroutine. A connection that briefly reaches
	// connected and then closes must never continue to WhatsApp accept/IVR.
	stateEvents := make(chan webrtc.PeerConnectionState, 16)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		m.log.Info("Peer connection state changed",
			"call_id", session.ID,
			"state", state.String(),
		)
		select {
		case stateEvents <- state:
		default:
		}
		// State callbacks only publish events. Cleanup has one owner in
		// endSession; calling it synchronously here makes Close() re-entrant.
	})

	// abortIfPeerTerminal checks whether the peer connection has already
	// died mid-negotiation and, if so, ends the call and reports true so the
	// caller can bail out immediately.
	//
	// Per the WhatsApp Business Calling API, "reject" is only a valid action
	// before the call has been pre-accepted; once pre_accept has been sent,
	// Meta considers the call in the process of being answered and expects
	// "terminate" instead. Sending "reject" after pre_accept/accept is a
	// protocol-level mismatch and — worse — because rejectCall's error path
	// only logs, it means Meta may never learn the call actually ended.
	// accepted must be true for every call site from "after_pre_accept"
	// onward.
	abortIfPeerTerminal := func(stage string, accepted bool) bool {
		state := pc.ConnectionState()
		if state != webrtc.PeerConnectionStateFailed && state != webrtc.PeerConnectionStateClosed {
			return false
		}
		// If PeerConnectionState is reported as failed, but the underlying ICE
		// connection is still alive (connected or checking), the DTLS handshake
		// may have failed temporarily because Meta's media server was still
		// processing the accept POST. Do not terminate the call prematurely if
		// ICE is still connected.
		iceState := pc.ICEConnectionState()
		if state == webrtc.PeerConnectionStateFailed && (iceState == webrtc.ICEConnectionStateConnected || iceState == webrtc.ICEConnectionStateChecking) {
			m.log.Info("PeerConnection state is failed, but ICE connection is still alive — ignoring premature terminal state",
				"call_id", session.ID,
				"stage", stage,
				"pc_state", state.String(),
				"ice_state", iceState.String(),
			)
			return false
		}
		m.log.Error("WebRTC peer became terminal during negotiation",
			"call_id", session.ID,
			"stage", stage,
			"state", state.String(),
			"ice_state", iceState.String(),
		)
		if accepted {
			m.signalTerminate(session, waAccount)
		} else {
			m.signalReject(session, waAccount)
		}
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return true
	}

	// Step 1: Set the consumer's SDP offer as remote description.
	//
	// DTLS role fix: Meta's SDP offer sends "a=setup:active", which tells Pion
	// to take the passive DTLS role (server). However, pion/dtls has a bug
	// where SetAnsweringDTLSRole(Server) is ignored when the remote SDP says
	// "active" — pion still starts as client and fires ClientHello immediately
	// when ICE connects. Meta's media server isn't ready yet (still processing
	// the AcceptCall POST, ~0.8s), drops the ClientHello, and Pion's DTLS
	// state machine fails → PeerConnectionState=failed within ~300ms.
	//
	// Fix: rewrite "a=setup:active" → "a=setup:actpass" in the remote SDP
	// before SetRemoteDescription. "actpass" means the remote is willing to
	// act as either client or server. Pion, configured with DTLSRoleServer,
	// will then correctly adopt the passive role and wait for Meta to send
	// the ClientHello — which only happens after AcceptCall completes, neatly
	// avoiding the race.
	patchedOffer := strings.ReplaceAll(sdpOffer, "a=setup:active", "a=setup:actpass")
	if patchedOffer != sdpOffer {
		m.log.Info("Patched remote SDP: a=setup:active → a=setup:actpass (DTLS passive role fix)", "call_id", session.ID)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  patchedOffer,
	}); err != nil {
		m.log.Error("Failed to set remote description (consumer offer)", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	// Step 2: Create SDP answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		m.log.Error("Failed to create SDP answer", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		m.log.Error("Failed to set local description (answer)", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	// Wait for ICE gathering to complete
	localDesc, err := waitForICEGathering(pc, 15*time.Second)
	if err != nil {
		m.log.Error("ICE gathering failed", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	sdpAnswer := localDesc.SDP

	if abortIfPeerTerminal("before_pre_accept", false) {
		return
	}

	// Step 3: Pre-accept with our SDP answer
	if err := m.whatsapp.PreAcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to pre-accept call", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}
	if abortIfPeerTerminal("after_pre_accept", true) {
		return
	}

	// Step 4: Accept with the same SDP answer
	if err := m.whatsapp.AcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to accept call via API", "error", err, "call_id", session.ID)
		// The failure may be purely local (e.g. our context was canceled
		// because the peer connection closed mid-request) even though the
		// POST already reached Meta's servers. Explicitly terminate on a
		// fresh context so we never leave Meta believing the call is live
		// while we've torn everything down locally — that would otherwise
		// leave the caller listening to dead air until Meta's own timeout.
		m.terminateCall(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}
	if abortIfPeerTerminal("after_accept", true) {
		return
	}

	session.mu.Lock()
	session.Status = models.CallStatusAnswered
	session.mu.Unlock()

	m.log.Info("Call accepted with WebRTC, waiting for media connection", "call_id", session.ID)

	// Wait for the WebRTC media connection to be established before starting IVR.
	// ICE connectivity checks run after the SDP exchange; we must wait for them
	// to complete before audio can flow.
	mediaTimer := time.NewTimer(30 * time.Second)
	defer mediaTimer.Stop()
	mediaConnected := false
	for !mediaConnected {
		select {
		case <-ctx.Done():
			m.log.Info("WebRTC negotiation cancelled for stale/ended call", "call_id", session.ID)
			return
		case state := <-stateEvents:
			switch state {
			case webrtc.PeerConnectionStateConnected:
				mediaConnected = true
			case webrtc.PeerConnectionStateFailed:
				iceState := pc.ICEConnectionState()
				if iceState == webrtc.ICEConnectionStateConnected || iceState == webrtc.ICEConnectionStateChecking || iceState == webrtc.ICEConnectionStateCompleted {
					m.log.Info("PeerConnection state changed to failed, but ICE is still active — waiting for media connection/DTLS recovery",
						"call_id", session.ID,
						"ice_state", iceState.String(),
					)
				} else {
					m.log.Error("WebRTC peer connection failed and ICE is not connected", "call_id", session.ID, "state", state.String(), "ice_state", iceState.String())
					m.terminateCall(session, waAccount)
					m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
					return
				}
			case webrtc.PeerConnectionStateClosed:
				m.log.Error("WebRTC peer closed before media became usable", "call_id", session.ID, "state", state.String())
				m.terminateCall(session, waAccount)
				m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
				return
			}
		case <-mediaTimer.C:
			// ICEConnectionState reaching "connected" only means STUN
			// connectivity checks passed; PeerConnectionState additionally
			// requires the DTLS-SRTP handshake to finish. If ICE connected but
			// we're still here, the handshake stalled on the selected pair —
			// almost always the host candidate, not the TURN relay pair. See
			// "Selected ICE candidate pair" log above for which pair was used.
			iceState := pc.ICEConnectionState()
			if iceState == webrtc.ICEConnectionStateConnected || iceState == webrtc.ICEConnectionStateCompleted {
				m.log.Error("WebRTC media connection timed out (ICE connected, DTLS handshake never completed — "+
					"likely a host-candidate NAT/MTU issue; consider WHATOMATE_CALLING__RELAY_ONLY=true)",
					"call_id", session.ID)
			} else {
				m.log.Error("WebRTC media connection timed out (ICE never connected)", "call_id", session.ID, "ice_state", iceState.String())
			}
			m.terminateCall(session, waAccount)
			m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
			return
		}
	}
	m.log.Info("WebRTC media connected", "call_id", session.ID)

	// Brief delay to ensure the selected ICE path remains stable before audio.
	// Ignore queued non-terminal state events instead of letting them bypass
	// the entire stabilization interval.
	stabilize := time.NewTimer(500 * time.Millisecond)
	defer stabilize.Stop()
stabilizeLoop:
	for {
		select {
		case <-ctx.Done():
			return
		case state := <-stateEvents:
			if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
				m.log.Error("WebRTC peer closed during media stabilization", "call_id", session.ID, "state", state.String())
				m.terminateCall(session, waAccount)
				m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
				return
			}
		case <-stabilize.C:
			break stabilizeLoop
		}
	}
	if abortIfPeerTerminal("before_ivr", true) {
		return
	}

	// Sticky-routed call: skip IVR entirely and ring the originating agent
	// directly via the existing transfer flow. initiateTransfer's
	// "ring-this-specific-agent-first" branch handles StickyAgentID; an
	// empty team target keeps the no-team broadcast as the eventual
	// fallback if the sticky agent doesn't answer.
	if session.StickyAgentID != nil {
		m.safeGo("initiateTransfer", session.ID, func() {
			m.initiateTransfer(session, session.AccountName, "", nil)
		})
	} else if session.IVRFlow != nil {
		m.safeGo("runIVRFlow", session.ID, func() {
			m.runIVRFlow(session, waAccount)
		})
	}

	// Negotiation used to return here, leaving no owner for disconnect or
	// failure events after IVR started. Keep a dedicated monitor alive.
	m.safeGo("monitorPeerConnection", session.ID, func() {
		m.monitorPeerConnection(session, stateEvents, waAccount)
	})
}

func (m *Manager) monitorPeerConnection(session *CallSession, stateEvents <-chan webrtc.PeerConnectionState, waAccount *whatsapp.Account) {
	ctx := session.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var disconnectTimer *time.Timer
	var disconnectC <-chan time.Time
	stopDisconnectTimer := func() {
		if disconnectTimer == nil {
			return
		}
		if !disconnectTimer.Stop() {
			select {
			case <-disconnectTimer.C:
			default:
			}
		}
		disconnectTimer = nil
		disconnectC = nil
	}
	defer stopDisconnectTimer()

	for {
		select {
		case <-ctx.Done():
			return
		case <-disconnectC:
			if !m.isCurrentSession(session) {
				return
			}
			m.log.Error("WebRTC peer remained disconnected past grace period", "call_id", session.ID)
			m.signalTerminate(session, waAccount)
			m.endSession(session, "peer_disconnected_timeout", "monitorPeerConnection")
			return
		case state := <-stateEvents:
			switch state {
			case webrtc.PeerConnectionStateConnected:
				stopDisconnectTimer()
			case webrtc.PeerConnectionStateDisconnected:
				if disconnectTimer == nil {
					disconnectTimer = time.NewTimer(8 * time.Second)
					disconnectC = disconnectTimer.C
				}
			case webrtc.PeerConnectionStateFailed:
				if !m.isCurrentSession(session) {
					return
				}
				// PeerConnectionState=failed with ICE still active is a
				// transient DTLS condition — give it a 6s grace window to
				// recover before terminating (same logic as the media-wait
				// loop in negotiateWebRTC).
				iceState := pc.ICEConnectionState()
				if iceState == webrtc.ICEConnectionStateConnected ||
					iceState == webrtc.ICEConnectionStateChecking ||
					iceState == webrtc.ICEConnectionStateCompleted {
					if disconnectTimer == nil {
						disconnectTimer = time.NewTimer(6 * time.Second)
						disconnectC = disconnectTimer.C
						m.log.Info("PeerConnection failed but ICE still active in monitor — starting grace timer",
							"call_id", session.ID, "ice_state", iceState.String())
					}
					continue
				}
				m.signalTerminate(session, waAccount)
				m.endSession(session, "peer_failed", "monitorPeerConnection")
				return
			case webrtc.PeerConnectionStateClosed:
				// Closed normally confirms our own cleanup. Only treat it as a
				// failure while this exact session is still the active map entry.
				if m.isCurrentSession(session) {
					m.log.Error("Active peer connection closed unexpectedly", "call_id", session.ID)
					m.signalTerminate(session, waAccount)
					m.endSession(session, "unexpected_peer_closed", "monitorPeerConnection")
				}
				return
			}
		}
	}
}

// waitForICEGathering waits for ICE gathering to complete on a PeerConnection
// and returns the local description, or an error on timeout.
func waitForICEGathering(pc *webrtc.PeerConnection, timeout time.Duration) (*webrtc.SessionDescription, error) {
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherComplete:
	case <-time.After(timeout):
		return nil, fmt.Errorf("ICE gathering timed out")
	}
	localDesc := pc.LocalDescription()
	if localDesc == nil {
		return nil, fmt.Errorf("no local description available")
	}
	return localDesc, nil
}

// createOpusTrack creates a new Opus audio track and adds it to the PeerConnection.
func createOpusTrack(pc *webrtc.PeerConnection, streamID string) (*webrtc.TrackLocalStaticRTP, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio",
		streamID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus track: %w", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		return nil, fmt.Errorf("failed to add opus track: %w", err)
	}
	return track, nil
}

// createPeerConnection creates a new WebRTC peer connection with Opus codec support
func (m *Manager) createPeerConnection() (*webrtc.PeerConnection, error) {
	now := time.Now()
	iceServers, err := m.resolveICEServers(now)
	if err != nil {
		return nil, fmt.Errorf("resolve ICE servers: %w", err)
	}
	if m.config.RelayOnly && len(iceServers) == 0 {
		return nil, fmt.Errorf("relay_only is enabled but no TURN server is configured")
	}

	config := webrtc.Configuration{
		ICEServers: iceServers,
	}

	// Force all media through TURN relay when direct UDP is not available.
	if m.config.RelayOnly {
		config.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}

	mediaEngine := &webrtc.MediaEngine{}

	// Register Opus codec
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("failed to register Opus codec: %w", err)
	}

	// Register telephone-event codec for DTMF (RFC 4733)
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  "audio/telephone-event",
			ClockRate: 8000,
		},
		PayloadType: 101,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("failed to register telephone-event codec: %w", err)
	}

	// Configure UDP port range and build API
	settingEngine := webrtc.SettingEngine{}
	portMin := m.config.UDPPortMin
	portMax := m.config.UDPPortMax
	if portMin == 0 {
		portMin = 10000
	}
	if portMax == 0 {
		// Widened from a 100-port default: each concurrent call holds one
		// local ephemeral UDP port for the life of the call (even in
		// relay-only/TURN mode, for the local ICE agent<->TURN allocation),
		// and slow OS-level port release under load can make a too-narrow
		// range look like "the next call fails" when it's really port
		// exhaustion. 1000 ports gives real headroom; override via
		// WHATOMATE_CALLING__UDP_PORT_MAX if the host needs a tighter range.
		portMax = 10999
	}
	if err := settingEngine.SetEphemeralUDPPortRange(portMin, portMax); err != nil {
		return nil, fmt.Errorf("failed to set UDP port range: %w", err)
	}

	// By default pion tears down the ENTIRE PeerConnection the instant its
	// internal DTLS endpoint reports "closed" (see pion/webrtc peerconnection.go,
	// dtlsTransport.internalOnCloseHandler). That handler doesn't only fire on a
	// genuine remote close_notify — it also fires when pion/ice swaps the
	// underlying net.Conn for the DTLS endpoint during ICE candidate-pair
	// renomination, which routinely happens within a few hundred ms of ICE first
	// reporting "connected" as the agent settles on its final pair. Every call in
	// production logs shows exactly that signature: ICE reaches "connected",
	// "Selected ICE candidate pair" is logged, and 200-700ms later the whole
	// PeerConnection is reported "closed" with no intervening "failed" state —
	// which per pion's updateConnectionState() is only possible when something
	// called pc.Close(), and nothing in our own code does so at that point. That
	// spurious close is what has been showing up as "WebRTC peer closed before
	// media became usable" and every call landing as "missed" with no IVR ever
	// running (negotiateWebRTC aborts before IVR starts). Disabling this
	// auto-close keeps the connection alive through the renomination; a truly
	// dead peer is still caught by the existing ICE failed/disconnected handling
	// in negotiateWebRTC's mediaConnected wait and in monitorPeerConnection.
	settingEngine.DisableCloseByDTLS(true)
	settingEngine.SetAnsweringDTLSRole(webrtc.DTLSRoleServer)

	// On cloud/AWS, map private IP to public IP so ICE candidates
	// advertise the reachable address instead of the internal one.
	// Skip when relay_only — host candidates are not used in relay mode.
	if m.config.PublicIP != "" && !m.config.RelayOnly {
		if err := settingEngine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{m.config.PublicIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, fmt.Errorf("failed to set ICE address rewrite rules: %w", err)
		}
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(settingEngine),
	)

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	// Debug: log ICE candidates and connection state to diagnose TURN issues.
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			m.log.Info("ICE gathering complete (no more candidates)")
			return
		}
		m.log.Info("ICE candidate gathered",
			"type", c.Typ.String(),
			"address", c.Address,
			"port", c.Port,
			"protocol", c.Protocol.String(),
			"related", c.RelatedAddress,
		)
	})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		m.log.Info("ICE connection state changed", "state", state.String())
		// Log which candidate pair ICE actually selected. Host-candidate pairs
		// routed through the Docker port-mapped bridge have shown unreliable
		// DTLS/SRTP establishment even after ICE itself reports "connected"
		// (see the media-timeout handling in negotiateWebRTC); this makes
		// that visible in logs instead of only inferring it after a timeout
		// or an unexplained "closed" transition. Logged as raw stats values
		// (not .String()) since exact field/method availability varies by
		// pion/webrtc version — the structured logger formats them fine.
		if state == webrtc.ICEConnectionStateConnected {
			go func() {
				for _, s := range pc.GetStats() {
					pair, ok := s.(webrtc.ICECandidatePairStats)
					if !ok || !pair.Nominated {
						continue
					}
					m.log.Info("Selected ICE candidate pair",
						"state", pair.State,
						"local_candidate_id", pair.LocalCandidateID,
						"remote_candidate_id", pair.RemoteCandidateID,
					)
				}
			}()
		}
	})

	return pc, nil
}

// consumeAudioTrack reads and discards RTP packets to keep the stream active.
// It exits when the bridge takes over (BridgeStarted channel is closed) or on error.
func (m *Manager) consumeAudioTrack(session *CallSession, track *webrtc.TrackRemote) {
	// A panic here would otherwise crash the whole process and take every
	// other active/future call down with it — see safety.go.
	defer m.recoverAndLog("consumeAudioTrack", session.ID)
	buf := make([]byte, 1500)
	for {
		select {
		case <-session.BridgeStarted:
			// Bridge is taking over reading from this track
			return
		default:
		}

		_, _, err := track.Read(buf)
		if err != nil {
			return
		}
	}
}

// consumeAudioWithDTMF reads RTP packets from the audio track, detecting
// inline telephone-event (DTMF) packets that share the same m-line.
// WhatsApp sends both Opus audio and telephone-event on a single track.
// In pion v4, a new OnTrack may fire for telephone-event, but we also
// handle the case where DTMF arrives on the same track.
func (m *Manager) consumeAudioWithDTMF(session *CallSession, track *webrtc.TrackRemote) {
	// A panic here would otherwise crash the whole process and take every
	// other active/future call down with it — see safety.go.
	defer m.recoverAndLog("consumeAudioWithDTMF", session.ID)
	audioPT := track.PayloadType()
	var lastDTMFEvent byte = 0xFF
	var lastEndBit bool
	packetCount := 0

	m.log.Info("Consuming audio with inline DTMF detection",
		"call_id", session.ID,
		"audio_pt", audioPT,
	)

	for {
		select {
		case <-session.BridgeStarted:
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			m.log.Debug("Audio track read ended", "call_id", session.ID, "error", err)
			return
		}

		packetCount++

		// Log every 500th packet and any non-audio packet for debugging
		if pkt.PayloadType != uint8(audioPT) {
			m.log.Info("Non-audio RTP packet received",
				"call_id", session.ID,
				"payload_type", pkt.PayloadType,
				"payload_len", len(pkt.Payload),
				"audio_pt", audioPT,
			)

			// Telephone-event DTMF payload is 4 bytes
			if len(pkt.Payload) >= 4 {
				eventID := pkt.Payload[0]
				endBit := (pkt.Payload[1] & 0x80) != 0

				if digit, ok := decodeDTMFEvent(eventID, endBit, &lastDTMFEvent, &lastEndBit); ok {
					m.log.Info("DTMF digit detected (inline)",
						"call_id", session.ID,
						"digit", string(digit),
						"event_id", eventID,
					)
					sendDTMFDigit(session, digit, m.log)
				}
			}
		} else if packetCount == 1 {
			m.log.Debug("First audio packet received",
				"call_id", session.ID,
				"payload_type", pkt.PayloadType,
			)
		}
	}
}

// rejectCall sends a reject action via the WhatsApp API
func (m *Manager) rejectCall(ctx context.Context, account *whatsapp.Account, callID string) {
	if err := m.whatsapp.RejectCall(ctx, account, callID); err != nil {
		m.log.Error("Failed to reject call", "error", err, "call_id", callID)
	}
}
