package websocket

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
	"github.com/zerodha/logf"
)

// Hub maintains the set of active clients and broadcasts messages to them.
type Hub struct {
	clients    map[uuid.UUID]map[uuid.UUID]map[*Client]struct{}
	broadcast  chan BroadcastMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
	log        logf.Logger
}

func NewHub(log logf.Logger) *Hub {
	return &Hub{
		clients:    make(map[uuid.UUID]map[uuid.UUID]map[*Client]struct{}),
		broadcast:  make(chan BroadcastMessage, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		log:        log,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)
		case client := <-h.unregister:
			h.unregisterClient(client)
		case message := <-h.broadcast:
			h.broadcastMessage(message)
		}
	}
}

func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	orgClients, ok := h.clients[client.organizationID]
	if !ok {
		orgClients = make(map[uuid.UUID]map[*Client]struct{})
		h.clients[client.organizationID] = orgClients
	}
	userClients, ok := orgClients[client.userID]
	if !ok {
		userClients = make(map[*Client]struct{})
		orgClients[client.userID] = userClients
	}
	userClients[client] = struct{}{}
	h.log.Info("WebSocket client registered",
		"user_id", client.userID,
		"org_id", client.organizationID,
		"user_connections", len(userClients),
		"total_clients", h.countClients())
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if orgClients, ok := h.clients[client.organizationID]; ok {
		if userClients, ok := orgClients[client.userID]; ok {
			if _, exists := userClients[client]; exists {
				delete(userClients, client)
				close(client.send)
				if len(userClients) == 0 {
					delete(orgClients, client.userID)
				}
				if len(orgClients) == 0 {
					delete(h.clients, client.organizationID)
				}
			}
		}
	}
	h.log.Info("WebSocket client unregistered",
		"user_id", client.userID,
		"org_id", client.organizationID,
		"total_clients", h.countClients())
}

func (h *Hub) broadcastMessage(msg BroadcastMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	orgClients, ok := h.clients[msg.OrgID]
	if !ok {
		return
	}
	data, err := json.Marshal(msg.Message)
	if err != nil {
		h.log.Error("Failed to marshal broadcast message", "error", err)
		return
	}
	if msg.UserID != uuid.Nil {
		userClients, ok := orgClients[msg.UserID]
		if !ok {
			return
		}
		for client := range userClients {
			select {
			case client.send <- data:
			default:
				h.log.Warn("Client send buffer full, skipping",
					"user_id", client.userID,
					"org_id", client.organizationID)
			}
		}
		return
	}
	for _, userClients := range orgClients {
		for client := range userClients {
			if msg.ContactID != uuid.Nil && client.currentContact != nil && *client.currentContact != msg.ContactID {
				continue
			}
			select {
			case client.send <- data:
			default:
				h.log.Warn("Client send buffer full, skipping",
					"user_id", client.userID,
					"org_id", client.organizationID)
			}
		}
	}
}

// Broadcast sends a message to WebSocket clients and mirrors call-routing
// lifecycle events to registered native devices. FCM delivery is asynchronous
// and never blocks/drops the realtime WebSocket path.
func (h *Hub) Broadcast(msg BroadcastMessage) {
	mirrorMobilePush(msg, h.log)
	select {
	case h.broadcast <- msg:
	default:
		h.log.Warn("Broadcast channel full, dropping message")
	}
}

func (h *Hub) BroadcastToOrg(orgID uuid.UUID, msg WSMessage) {
	h.Broadcast(BroadcastMessage{OrgID: orgID, Message: msg})
}

func (h *Hub) BroadcastToContact(orgID, contactID uuid.UUID, msg WSMessage) {
	h.Broadcast(BroadcastMessage{OrgID: orgID, ContactID: contactID, Message: msg})
}

func (h *Hub) BroadcastToUser(orgID, userID uuid.UUID, msg WSMessage) {
	h.Broadcast(BroadcastMessage{OrgID: orgID, UserID: userID, Message: msg})
}

func (h *Hub) BroadcastToUsers(orgID uuid.UUID, userIDs []uuid.UUID, msg WSMessage) {
	for _, userID := range userIDs {
		h.BroadcastToUser(orgID, userID, msg)
	}
}

func (h *Hub) countClients() int {
	count := 0
	for _, orgClients := range h.clients {
		for _, userClients := range orgClients {
			count += len(userClients)
		}
	}
	return count
}

func (h *Hub) GetClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.countClients()
}

func (h *Hub) IsUserOnline(orgID, userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if orgClients, ok := h.clients[orgID]; ok {
		if userClients, ok := orgClients[userID]; ok {
			return len(userClients) > 0
		}
	}
	return false
}

func (h *Hub) OnlineUserIDs(orgID uuid.UUID) []uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	orgClients, ok := h.clients[orgID]
	if !ok {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(orgClients))
	for uid, clients := range orgClients {
		if len(clients) > 0 {
			ids = append(ids, uid)
		}
	}
	return ids
}

func (h *Hub) FilterOnlineUsers(orgID uuid.UUID, userIDs []uuid.UUID) []uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	orgClients, ok := h.clients[orgID]
	if !ok {
		return nil
	}
	online := make([]uuid.UUID, 0, len(userIDs))
	for _, uid := range userIDs {
		if userClients, ok := orgClients[uid]; ok && len(userClients) > 0 {
			online = append(online, uid)
		}
	}
	return online
}

func (h *Hub) Register(client *Client)   { h.register <- client }
func (h *Hub) Unregister(client *Client) { h.unregister <- client }
