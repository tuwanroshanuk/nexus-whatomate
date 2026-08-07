package calling

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// negotiateWebRTC handles the SDP exchange and sets up WebRTC media for
// WhatsApp user-initiated calls.
func (m *Manager) negotiateWebRTC(session *CallSession, account *models.WhatsAppAccount, sdpOffer string, callerPhone string) {
	defer m.recoverAndLog("negotiateWebRTC", session.ID)

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

	// Meta's Calling media endpoint is ICE-LITE and expects the business media
	// endpoint to be the DTLS client for user-initiated calls. Use a dedicated
	// answerer configuration here instead of the generic server-role answerer
	// used by browser-facing peer connections.
	pc, err := m.createInboundWhatsAppPeerConnection()
	if err != nil {
		m.log.Error("Failed to create peer connection", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

	session.mu.Lock()
	session.PeerConnection = pc
	session.mu.Unlock()

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

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		m.log.Info("Received remote track",
			"call_id", session.ID,
			"codec", track.Codec().MimeType,
			"payload_type", track.PayloadType(),
		)

		if track.Codec().MimeType == "audio/telephone-event" {
			m.safeGo("handleDTMFTrack", session.ID, func() {
				m.handleDTMFTrack(session, track)
			})
			return
		}

		session.mu.Lock()
		session.CallerRemoteTrack = track
		bridge := session.Bridge
		agentLocal := session.AgentAudioTrack
		session.mu.Unlock()

		if bridge != nil && agentLocal != nil {
			bridge.AttachCaller(track, agentLocal)
			return
		}

		m.safeGo("consumeAudioWithDTMF", session.ID, func() {
			m.consumeAudioWithDTMF(session, track)
		})
	})

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
	})

	abortIfPeerTerminal := func(stage string, accepted bool) bool {
		state := pc.ConnectionState()
		if state != webrtc.PeerConnectionStateFailed && state != webrtc.PeerConnectionStateClosed {
			return false
		}
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

	// Do not rewrite Meta's SDP setup attribute. The offer is authoritative.
	// Rewriting setup:active to actpass changes the DTLS role contract and can
	// produce the exact ICE-connected/DTLS-failed sequence seen in production.
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}); err != nil {
		m.log.Error("Failed to set remote description (consumer offer)", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}

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

	// Pre-accept first so ICE/DTLS can establish before audio is emitted.
	if err := m.whatsapp.PreAcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to pre-accept call", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
		return
	}
	if abortIfPeerTerminal("after_pre_accept", true) {
		return
	}

	// Meta requires the exact same SDP answer for accept as pre_accept.
	if err := m.whatsapp.AcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to accept call via API", "error", err, "call_id", session.ID)
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

	mediaTimer := time.NewTimer(15 * time.Second)
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
					m.log.Info("PeerConnection state changed to failed, but ICE is still active — waiting briefly for DTLS recovery",
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
			iceState := pc.ICEConnectionState()
			m.log.Error("WebRTC media connection timed out", "call_id", session.ID, "ice_state", iceState.String(), "pc_state", pc.ConnectionState().String())
			m.terminateCall(session, waAccount)
			m.endSession(session, "webrtc_terminal", "negotiateWebRTC")
			return
		}
	}
	m.log.Info("WebRTC media connected; starting call media", "call_id", session.ID)

	// No artificial post-connect delay. Once PeerConnectionState is connected,
	// DTLS-SRTP is ready and the IVR can start immediately, GSM-style.
	if abortIfPeerTerminal("before_ivr", true) {
		return
	}

	if session.StickyAgentID != nil {
		m.safeGo("initiateTransfer", session.ID, func() {
			m.initiateTransfer(session, session.AccountName, "", nil)
		})
	} else if session.IVRFlow != nil {
		m.safeGo("runIVRFlow", session.ID, func() {
			m.runIVRFlow(session, waAccount)
		})
	}

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

				session.mu.Lock()
				peer := session.PeerConnection
				session.mu.Unlock()
				if peer == nil {
					m.signalTerminate(session, waAccount)
					m.endSession(session, "peer_failed", "monitorPeerConnection")
					return
				}

				iceState := peer.ICEConnectionState()
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

func (m *Manager) createPeerConnection() (*webrtc.PeerConnection, error) {
	return m.createPeerConnectionWithAnswerDTLSRole(webrtc.DTLSRoleServer)
}

// createInboundWhatsAppPeerConnection is used when answering Meta's UIC SDP
// offer. Meta expects the business endpoint to take the active/client DTLS role.
func (m *Manager) createInboundWhatsAppPeerConnection() (*webrtc.PeerConnection, error) {
	return m.createPeerConnectionWithAnswerDTLSRole(webrtc.DTLSRoleClient)
}

func (m *Manager) createPeerConnectionWithAnswerDTLSRole(answerDTLSRole webrtc.DTLSRole) (*webrtc.PeerConnection, error) {
	now := time.Now()
	iceServers, err := m.resolveICEServers(now)
	if err != nil {
		return nil, fmt.Errorf("resolve ICE servers: %w", err)
	}
	if m.config.RelayOnly && len(iceServers) == 0 {
		return nil, fmt.Errorf("relay_only is enabled but no TURN server is configured")
	}

	config := webrtc.Configuration{ICEServers: iceServers}
	if m.config.RelayOnly {
		config.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}

	mediaEngine := &webrtc.MediaEngine{}
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

	// Meta's current Calling SDP guidance uses telephone-event PT 126/8000.
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  "audio/telephone-event",
			ClockRate: 8000,
		},
		PayloadType: 126,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("failed to register telephone-event codec: %w", err)
	}

	settingEngine := webrtc.SettingEngine{}
	portMin := m.config.UDPPortMin
	portMax := m.config.UDPPortMax
	if portMin == 0 {
		portMin = 10000
	}
	if portMax == 0 {
		portMax = 10999
	}
	if err := settingEngine.SetEphemeralUDPPortRange(portMin, portMax); err != nil {
		return nil, fmt.Errorf("failed to set UDP port range: %w", err)
	}

	settingEngine.DisableCloseByDTLS(true)
	settingEngine.SetAnsweringDTLSRole(answerDTLSRole)

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

func (m *Manager) consumeAudioTrack(session *CallSession, track *webrtc.TrackRemote) {
	defer m.recoverAndLog("consumeAudioTrack", session.ID)
	buf := make([]byte, 1500)
	for {
		select {
		case <-session.BridgeStarted:
			return
		default:
		}

		_, _, err := track.Read(buf)
		if err != nil {
			return
		}
	}
}

func (m *Manager) consumeAudioWithDTMF(session *CallSession, track *webrtc.TrackRemote) {
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
		if pkt.PayloadType != uint8(audioPT) {
			m.log.Info("Non-audio RTP packet received",
				"call_id", session.ID,
				"payload_type", pkt.PayloadType,
				"payload_len", len(pkt.Payload),
				"audio_pt", audioPT,
			)

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

func (m *Manager) rejectCall(ctx context.Context, account *whatsapp.Account, callID string) {
	if err := m.whatsapp.RejectCall(ctx, account, callID); err != nil {
		m.log.Error("Failed to reject call", "error", err, "call_id", callID)
	}
}
