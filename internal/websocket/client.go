package websocket

import (
	"encoding/json"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 2048
	authTimeout    = 5 * time.Second
)

// AuthenticateFn validates a JWT token and returns user ID and organization ID.
type AuthenticateFn func(token string) (uuid.UUID, uuid.UUID, error)

// RegisterPushFn persists a native device token for the authenticated socket.
type RegisterPushFn func(userID, orgID uuid.UUID, payload PushRegistrationPayload) error

// UnregisterPushFn removes a native device token for the authenticated socket.
type UnregisterPushFn func(userID, orgID uuid.UUID, token string) error

// Client represents a WebSocket client connection.
type Client struct {
	hub              *Hub
	conn             *websocket.Conn
	send             chan []byte
	userID           uuid.UUID
	organizationID   uuid.UUID
	authenticated    bool
	authFn           AuthenticateFn
	registerPushFn   RegisterPushFn
	unregisterPushFn UnregisterPushFn
	currentContact   *uuid.UUID
}

func NewClient(hub *Hub, conn *websocket.Conn, userID, orgID uuid.UUID) *Client {
	return &Client{
		hub:            hub,
		conn:           conn,
		send:           make(chan []byte, 256),
		userID:         userID,
		organizationID: orgID,
		authenticated:  userID != uuid.Nil,
	}
}

// NewUnauthenticatedClient creates a client that requires message-based auth.
// Optional push callbacks keep existing tests/callers source-compatible.
func NewUnauthenticatedClient(hub *Hub, conn *websocket.Conn, authFn AuthenticateFn, callbacks ...any) *Client {
	client := &Client{
		hub:    hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		authFn: authFn,
	}
	for _, callback := range callbacks {
		switch fn := callback.(type) {
		case RegisterPushFn:
			client.registerPushFn = fn
		case UnregisterPushFn:
			client.unregisterPushFn = fn
		}
	}
	return client
}

func (c *Client) ReadPump() {
	defer func() {
		if r := recover(); r != nil {
			c.hub.log.Error("Recovered from panic in ReadPump", "error", r, "user_id", c.userID)
		}
		if c.authenticated {
			c.hub.unregister <- c
		} else {
			close(c.send)
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
	}()

	c.conn.SetReadLimit(maxMessageSize)
	if !c.authenticated {
		_ = c.conn.SetReadDeadline(time.Now().Add(authTimeout))
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.hub.log.Warn("WebSocket auth timeout or read error", "error", err)
			return
		}
		if !c.handleAuthMessage(message) {
			_ = c.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication failed"))
			return
		}
	}

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.log.Error("WebSocket read error", "error", err, "user_id", c.userID)
			}
			break
		}
		c.handleMessage(message)
	}
}

func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		if r := recover(); r != nil {
			c.hub.log.Error("Recovered from panic in WritePump", "error", r, "user_id", c.userID)
		}
		ticker.Stop()
		if c.conn != nil {
			_ = c.conn.Close()
		}
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok || c.conn == nil {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !c.authenticated {
				continue
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
			n := len(c.send)
			for i := 0; i < n; i++ {
				if err := c.conn.WriteMessage(websocket.TextMessage, <-c.send); err != nil {
					return
				}
			}
		case <-ticker.C:
			if c.conn == nil {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) handleAuthMessage(data []byte) bool {
	var msg WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.hub.log.Error("Failed to unmarshal auth message", "error", err)
		return false
	}
	if msg.Type != TypeAuth {
		c.hub.log.Warn("Expected auth message, got", "type", msg.Type)
		return false
	}
	payloadBytes, err := json.Marshal(msg.Payload)
	if err != nil {
		return false
	}
	var authPayload AuthPayload
	if err := json.Unmarshal(payloadBytes, &authPayload); err != nil {
		return false
	}
	if authPayload.Token == "" || c.authFn == nil {
		return false
	}
	userID, orgID, err := c.authFn(authPayload.Token)
	if err != nil {
		c.hub.log.Warn("WebSocket auth failed", "error", err)
		return false
	}
	c.userID = userID
	c.organizationID = orgID
	c.authenticated = true
	c.hub.Register(c)
	c.hub.log.Info("WebSocket client authenticated via message",
		"user_id", userID, "org_id", orgID)
	return true
}

func (c *Client) handleMessage(data []byte) {
	var msg WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.hub.log.Error("Failed to unmarshal client message", "error", err)
		return
	}
	switch msg.Type {
	case TypeSetContact:
		c.handleSetContact(msg.Payload)
	case TypePing:
		c.sendPong()
	case TypeRegisterPushToken:
		c.handleRegisterPush(msg.Payload)
	case TypeUnregisterPushToken:
		c.handleUnregisterPush(msg.Payload)
	}
}

func (c *Client) handleSetContact(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var setContact SetContactPayload
	if err := json.Unmarshal(data, &setContact); err != nil {
		return
	}
	if setContact.ContactID == "" {
		c.currentContact = nil
		c.hub.log.Debug("Client cleared current contact", "user_id", c.userID)
	} else {
		contactID, err := uuid.Parse(setContact.ContactID)
		if err != nil {
			return
		}
		c.currentContact = &contactID
		c.hub.log.Debug("Client set current contact", "user_id", c.userID, "contact_id", contactID)
	}
}

func (c *Client) handleRegisterPush(payload any) {
	if c.registerPushFn == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var registration PushRegistrationPayload
	if err := json.Unmarshal(data, &registration); err != nil || registration.Token == "" {
		return
	}
	if err := c.registerPushFn(c.userID, c.organizationID, registration); err != nil {
		c.hub.log.Warn("Failed to register mobile push token", "error", err, "user_id", c.userID)
		return
	}
	ack, _ := json.Marshal(WSMessage{Type: TypePushRegistered, Payload: map[string]any{"registered": true}})
	select {
	case c.send <- ack:
	default:
	}
}

func (c *Client) handleUnregisterPush(payload any) {
	if c.unregisterPushFn == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Token == "" {
		return
	}
	if err := c.unregisterPushFn(c.userID, c.organizationID, body.Token); err != nil {
		c.hub.log.Warn("Failed to unregister mobile push token", "error", err, "user_id", c.userID)
	}
}

func (c *Client) SendChan() <-chan []byte { return c.send }

func (c *Client) sendPong() {
	msg := WSMessage{Type: TypePong}
	data, _ := json.Marshal(msg)
	select {
	case c.send <- data:
	default:
	}
}
