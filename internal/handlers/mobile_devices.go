package handlers

import (
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// RegisterMobileDevice persists the current native installation for FCM call
// delivery. User and organization identity always come from the authenticated
// request context; callers cannot register a token for another user or tenant.
//
// WebSocket registration remains supported for realtime clients, but this REST
// endpoint deliberately makes durable mobile reachability independent of a
// realtime connection. That is important for Android-only agents and for FCM
// delivery while the app process is backgrounded or not running.
func (a *App) RegisterMobileDevice(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	var payload ws.PushRegistrationPayload
	if err := a.decodeRequest(r, &payload); err != nil {
		return nil
	}
	if payload.Token == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "push token is required", nil, "")
	}
	if err := ws.RegisterMobileDevice(a.DB, a.Log, userID, orgID, payload); err != nil {
		a.Log.Warn("Failed to register mobile device", "error", err, "user_id", userID, "org_id", orgID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to register mobile device", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"registered": true,
		"platform":   payload.Platform,
	})
}

// UnregisterMobileDevice removes the current native installation from call
// routing. The token can only be deleted for the authenticated user/org.
func (a *App) UnregisterMobileDevice(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := a.decodeRequest(r, &body); err != nil {
		return nil
	}
	if body.Token == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "push token is required", nil, "")
	}
	if err := ws.UnregisterMobileDevice(a.DB, body.Token, userID, orgID); err != nil {
		a.Log.Warn("Failed to unregister mobile device", "error", err, "user_id", userID, "org_id", orgID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to unregister mobile device", nil, "")
	}

	return r.SendEnvelope(map[string]bool{"unregistered": true})
}
