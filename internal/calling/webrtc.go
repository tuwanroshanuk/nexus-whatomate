package calling

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// negotiateWebRTC answers a WhatsApp user-initiated call.
//
// Correct media order for UIC:
//   1. Apply Meta's SDP offer.
//   2. Generate our SDP answer.
//   3. pre_accept with that answer.
//   4. Establish ICE + DTLS-SRTP.
//   5. accept with the exact same SDP answer.
//   6. Start the resident IVR media stream immediately.
//
// Accepting before step 4 can make the Android client briefly display a
// connected call while DTLS is still unresolved, then terminate with
// "Couldn't make call". The media connection is therefore the gate for accept.
func (m *Manager) negotiateWebRTC(session *CallSession, account *models.WhatsAppAccount, sdpOffer string, callerPhone string) {
	defer m.recoverAndLog("negotiateWebRTC", session.ID)

	const callerCooldownWindow = 350 * time.Millisecond
	if callerPhone != "" {
		m.cooldownMu.Lock()
		last, ok := m.callerCooldowns[callerPhone]
		m.cooldownMu.Unlock()
		if ok {
			elapsed := time.Since(last)
			if elapsed < callerCooldownWindow {
				time.Sleep(callerCooldownWindow - elapsed)
			}
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

	pc, err := m.createInboundWhatsAppPeerConnection()
	if err != nil {
		m.log.Error("Failed to create WhatsApp peer connection", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "webrtc_create_failed", "negotiateWebRTC")
		return
	}

	session.mu.Lock()
	session.PeerConnection = pc
	session.mu.Unlock()

	audioTrack, err := createOpusTrack(pc, "ivr-audio")
	if err != nil {
		m.log.Error("Failed to create IVR audio track", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "audio_track_failed", "negotiateWebRTC")
		return
	}

	// Create the player before negotiation completes. The resident media cache
	// is already hot; after DTLS connects we can prime RTP and enter the graph
	// without constructing another realtime media object.
	player := NewAudioPlayer(audioTrack)
	session.mu.Lock()
	session.AudioTrack = audioTrack
	session.IVRPlayer = player
	session.mu.Unlock()

	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		m.log.Info("Received WhatsApp remote track",
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

	stateEvents := make(chan webrtc.PeerConnectionState, 32)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		m.log.Info("Peer connection state changed", "call_id", session.ID, "state", state.String())
		select {
		case stateEvents <- state:
		default:
		}
	})

	// Meta's offer is authoritative. Do not rewrite a=setup because that changes
	// the negotiated DTLS role contract.
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}); err != nil {
		m.log.Error("Failed to set WhatsApp SDP offer", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "remote_sdp_failed", "negotiateWebRTC")
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		m.log.Error("Failed to create SDP answer", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "answer_failed", "negotiateWebRTC")
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		m.log.Error("Failed to set SDP answer", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "local_sdp_failed", "negotiateWebRTC")
		return
	}

	localDesc, err := waitForICEGathering(pc, 12*time.Second)
	if err != nil {
		m.log.Error("ICE gathering failed", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "ice_gather_failed", "negotiateWebRTC")
		return
	}
	sdpAnswer := localDesc.SDP

	// pre_accept starts early media negotiation. The same answer must later be
	// supplied to accept.
	if err := m.whatsapp.PreAcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to pre-accept call", "error", err, "call_id", session.ID)
		m.signalReject(session, waAccount)
		m.endSession(session, "pre_accept_failed", "negotiateWebRTC")
		return
	}
	m.log.Info("Call pre-accepted; waiting for secure media before accept", "call_id", session.ID)

	// Media must be connected before final accept. PeerConnectionStateConnected
	// means ICE + DTLS + SCTP/media transports are established, not merely that
	// a STUN pair nominated successfully.
	mediaCtx, mediaCancel := context.WithTimeout(ctx, 12*time.Second)
	defer mediaCancel()
	if !waitForPeerConnected(mediaCtx, pc, stateEvents, m, session) {
		m.log.Error("Secure media did not establish after pre-accept",
			"call_id", session.ID,
			"pc_state", pc.ConnectionState().String(),
			"ice_state", pc.ICEConnectionState().String(),
		)
		m.signalTerminate(session, waAccount)
		m.endSession(session, "media_connect_failed", "negotiateWebRTC")
		return
	}

	m.log.Info("Secure media connected; accepting call", "call_id", session.ID)
	if err := m.whatsapp.AcceptCall(ctx, waAccount, session.ID, sdpAnswer); err != nil {
		m.log.Error("Failed to accept call", "error", err, "call_id", session.ID)
		m.terminateCall(session, waAccount)
		m.endSession(session, "accept_failed", "negotiateWebRTC")
		return
	}

	session.mu.Lock()
	session.Status = models.CallStatusAnswered
	session.mu.Unlock()

	// Establish a live outbound RTP clock immediately. Three Opus silence frames
	// cost ~60ms and avoid the first greeting packet being the first media packet
	// seen by the WhatsApp client.
	if err := player.Prime(3); err != nil {
		m.log.Error("Failed to prime IVR RTP stream", "error", err, "call_id", session.ID)
		m.terminateCall(session, waAccount)
		m.endSession(session, "media_prime_failed", "negotiateWebRTC")
		return
	}

	m.log.Info("Call accepted; realtime IVR media active", "call_id", session.ID)

	if session.StickyAgentID != nil {
		m.safeGo("initiateTransfer", session.ID, func() {
			m.initiateTransfer(session, session.AccountName, "", nil)
		})
	} else if session.IVRFlow != nil {
		m.safeGo("runIVRFlow", session.ID, func() {
			m.runIVRFlow(session, waAccount)
		})
	} else {
		m.log.Warn("Accepted call has no IVR flow and no sticky agent", "call_id", session.ID)
	}

	m.safeGo("monitorPeerConnection", session.ID, func() {
		m.monitorPeerConnection(session, stateEvents, waAccount)
	})
}

func waitForPeerConnected(ctx context.Context, pc *webrtc.PeerConnection, stateEvents <-chan webrtc.PeerConnectionState, m *Manager, session *CallSession) bool {
	if pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return false
		case state := <-stateEvents:
			switch state {
			case webrtc.PeerConnectionStateConnected:
				return true
			case webrtc.PeerConnectionStateFailed:
				// A failed PeerConnection with ICE still connected is not usable
				// media. Do not pretend it recovered merely because STUN succeeded;
				// give Pion a short opportunity to emit Connected, otherwise the
				// outer timeout terminates the call cleanly.
				m.log.Warn("DTLS/media not ready although ICE is active",
					"call_id", session.ID,
					"ice_state", pc.ICEConnectionState().String(),
				)
			case webrtc.PeerConnectionStateClosed:
				return false
			}
		}
	}
}

func (m *Manager) monitorPeerConnection(session *CallSession, stateEvents <-chan webrtc.PeerConnectionState, waAccount *whatsapp.Account) {
	ctx := session.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var disconnectTimer *time.Timer
	var disconnectC <-chan time.Time
	stopTimer := func() {
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
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return
		case <-disconnectC:
			if !m.isCurrentSession(session) {
				return
			}
			m.log.Error("WebRTC peer remained unhealthy past grace period", "call_id", session.ID)
			m.signalTerminate(session, waAccount)
			m.endSession(session, "peer_timeout", "monitorPeerConnection")
			return
		case state := <-stateEvents:
			switch state {
			case webrtc.PeerConnectionStateConnected:
				stopTimer()
			case webrtc.PeerConnectionStateDisconnected:
				if disconnectTimer == nil {
					disconnectTimer = time.NewTimer(8 * time.Second)
					disconnectC = disconnectTimer.C
				}
			case webrtc.PeerConnectionStateFailed:
				if !m.isCurrentSession(session) {
					return
				}
				if disconnectTimer == nil {
					disconnectTimer = time.NewTimer(4 * time.Second)
					disconnectC = disconnectTimer.C
				}
			case webrtc.PeerConnectionStateClosed:
				if m.isCurrentSession(session) {
					m.signalTerminate(session, waAccount)
					m.endSession(session, "peer_closed", "monitorPeerConnection")
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
		return nil, fmt.Errorf("failed to create Opus track: %w", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		return nil, fmt.Errorf("failed to add Opus track: %w", err)
	}
	return track, nil
}

// createPeerConnection is the generic browser/outgoing-call WebRTC endpoint.
func (m *Manager) createPeerConnection() (*webrtc.PeerConnection, error) {
	return m.createPeerConnectionWithAnswerDTLSRole(webrtc.DTLSRoleServer)
}

// Meta's media side is ICE-LITE. The business media endpoint is ICE-FULL and
// acts as DTLS client for the inbound/UIC answer path.
func (m *Manager) createInboundWhatsAppPeerConnection() (*webrtc.PeerConnection, error) {
	return m.createPeerConnectionWithAnswerDTLSRole(webrtc.DTLSRoleClient)
}

func (m *Manager) createPeerConnectionWithAnswerDTLSRole(answerDTLSRole webrtc.DTLSRole) (*webrtc.PeerConnection, error) {
	iceServers, err := m.resolveICEServers(time.Now())
	if err != nil {
		return nil, fmt.Errorf("resolve ICE servers: %w", err)
	}
	if m.config.RelayOnly && len(iceServers) == 0 {
		return nil, fmt.Errorf("relay_only is enabled but no TURN server is configured")
	}

	cfg := webrtc.Configuration{ICEServers: iceServers}
	if m.config.RelayOnly {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
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
		return nil, fmt.Errorf("register Opus codec: %w", err)
	}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  "audio/telephone-event",
			ClockRate: 8000,
		},
		PayloadType: 126,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register telephone-event codec: %w", err)
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
		return nil, fmt.Errorf("set UDP port range: %w", err)
	}

	// Keep explicit ownership of peer teardown. A transient DTLS close callback
	// must not tear down the whole application-level call session underneath the
	// lifecycle manager.
	settingEngine.DisableCloseByDTLS(true)
	settingEngine.SetAnsweringDTLSRole(answerDTLSRole)

	if m.config.PublicIP != "" && !m.config.RelayOnly {
		if err := settingEngine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{m.config.PublicIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, fmt.Errorf("set ICE address rewrite rules: %w", err)
		}
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(settingEngine),
	)
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			m.log.Info("ICE gathering complete")
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
		if state != webrtc.ICEConnectionStateConnected && state != webrtc.ICEConnectionStateCompleted {
			return
		}
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
		if _, _, err := track.Read(buf); err != nil {
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

	for {
		select {
		case <-session.BridgeStarted:
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		packetCount++

		if pkt.PayloadType != uint8(audioPT) && len(pkt.Payload) >= 4 {
			eventID := pkt.Payload[0]
			endBit := (pkt.Payload[1] & 0x80) != 0
			if digit, ok := decodeDTMFEvent(eventID, endBit, &lastDTMFEvent, &lastEndBit); ok {
				m.log.Info("DTMF digit detected", "call_id", session.ID, "digit", string(digit), "event_id", eventID)
				sendDTMFDigit(session, digit, m.log)
			}
		} else if packetCount == 1 {
			m.log.Debug("First WhatsApp audio packet received", "call_id", session.ID, "payload_type", pkt.PayloadType)
		}
	}
}

func (m *Manager) rejectCall(ctx context.Context, account *whatsapp.Account, callID string) {
	if err := m.whatsapp.RejectCall(ctx, account, callID); err != nil {
		m.log.Error("Failed to reject call", "error", err, "call_id", callID)
	}
}
