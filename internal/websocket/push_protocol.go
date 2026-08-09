package websocket

const (
	TypeRegisterPushToken   = "register_push_token"
	TypeUnregisterPushToken = "unregister_push_token"
	TypePushRegistered      = "push_registered"
)

// PushRegistrationPayload is sent by native clients after WebSocket auth.
// Registration is deliberately attached to the authenticated socket so user
// and organization IDs always come from validated server-side claims.
type PushRegistrationPayload struct {
	Token      string `json:"token"`
	Platform   string `json:"platform"`
	DeviceName string `json:"device_name,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}
