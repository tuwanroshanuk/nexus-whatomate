package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zerodha/logf"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"gorm.io/gorm"
)

const firebaseMessagingScope = "https://www.googleapis.com/auth/firebase.messaging"

// MobileDevice is a registered native app installation. The FCM registration
// token is unique globally while user/org are retained so WebSocket routing and
// mobile push routing can use the same authorization scope.
type MobileDevice struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_mobile_device_org_user" json:"organization_id"`
	UserID         uuid.UUID `gorm:"type:uuid;not null;index:idx_mobile_device_org_user" json:"user_id"`
	Token          string    `gorm:"type:text;not null;uniqueIndex" json:"-"`
	Platform       string    `gorm:"size:32;not null" json:"platform"`
	DeviceName     string    `gorm:"size:255" json:"device_name"`
	AppVersion     string    `gorm:"size:64" json:"app_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

func (m *MobileDevice) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

type mobilePushService struct {
	db        *gorm.DB
	log       logf.Logger
	projectID string
	client    *http.Client
	enabled   bool
}

var mobilePushState struct {
	sync.RWMutex
	service *mobilePushService
	db      *gorm.DB
}

// EnsureMobilePush attaches the server database to the mobile push subsystem.
// It is safe to call on every WebSocket connection and every incoming call.
// The first call performs the idempotent device-table migration and initializes
// FCM from environment configuration.
func EnsureMobilePush(db *gorm.DB, log logf.Logger) {
	if db == nil {
		return
	}
	mobilePushState.RLock()
	ready := mobilePushState.service != nil && mobilePushState.db == db
	mobilePushState.RUnlock()
	if ready {
		return
	}

	mobilePushState.Lock()
	defer mobilePushState.Unlock()
	if mobilePushState.service != nil && mobilePushState.db == db {
		return
	}

	if err := db.AutoMigrate(&MobileDevice{}); err != nil {
		log.Error("Failed to migrate mobile device registrations", "error", err)
		return
	}

	service := &mobilePushService{db: db, log: log}
	service.configureFCM()
	mobilePushState.db = db
	mobilePushState.service = service
}

func (s *mobilePushService) configureFCM() {
	projectID := strings.TrimSpace(os.Getenv("WHATOMATE_FIREBASE_PROJECT_ID"))
	credentialsJSON := strings.TrimSpace(os.Getenv("WHATOMATE_FIREBASE_CREDENTIALS_JSON"))
	credentialsFile := strings.TrimSpace(os.Getenv("WHATOMATE_FIREBASE_CREDENTIALS_FILE"))

	if credentialsJSON == "" && credentialsFile != "" {
		if b, err := os.ReadFile(credentialsFile); err == nil {
			credentialsJSON = string(b)
		} else {
			s.log.Warn("Unable to read Firebase credentials file", "error", err)
		}
	}

	ctx := context.Background()
	var creds *google.Credentials
	var err error
	if credentialsJSON != "" {
		creds, err = google.CredentialsFromJSON(ctx, []byte(credentialsJSON), firebaseMessagingScope)
	} else {
		creds, err = google.FindDefaultCredentials(ctx, firebaseMessagingScope)
	}
	if err != nil {
		s.log.Info("Mobile FCM push disabled; Firebase credentials are not configured")
		return
	}
	if projectID == "" {
		projectID = creds.ProjectID
	}
	if projectID == "" {
		s.log.Warn("Mobile FCM push disabled; WHATOMATE_FIREBASE_PROJECT_ID is empty")
		return
	}

	s.projectID = projectID
	s.client = oauth2.NewClient(ctx, creds.TokenSource)
	s.client.Timeout = 12 * time.Second
	s.enabled = true
	s.log.Info("Mobile FCM push initialized", "project_id", projectID)
}

// RegisterMobileDevice upserts a device token for the authenticated WebSocket
// user. A token that moves between users/orgs is deliberately reassigned so a
// signed-out installation cannot continue receiving the previous tenant's calls.
func RegisterMobileDevice(db *gorm.DB, log logf.Logger, userID, orgID uuid.UUID, payload PushRegistrationPayload) error {
	EnsureMobilePush(db, log)
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		return fmt.Errorf("push token is required")
	}
	platform := strings.ToLower(strings.TrimSpace(payload.Platform))
	if platform == "" {
		platform = "android"
	}
	now := time.Now().UTC()
	device := MobileDevice{
		ID:             uuid.New(),
		OrganizationID: orgID,
		UserID:         userID,
		Token:          token,
		Platform:       platform,
		DeviceName:     strings.TrimSpace(payload.DeviceName),
		AppVersion:     strings.TrimSpace(payload.AppVersion),
		LastSeenAt:     now,
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var existing MobileDevice
		err := tx.Where("token = ?", token).First(&existing).Error
		if err == nil {
			return tx.Model(&existing).Updates(map[string]any{
				"organization_id": orgID,
				"user_id":         userID,
				"platform":        platform,
				"device_name":     device.DeviceName,
				"app_version":     device.AppVersion,
				"last_seen_at":    now,
			}).Error
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		return tx.Create(&device).Error
	})
}

func UnregisterMobileDevice(db *gorm.DB, token string, userID, orgID uuid.UUID) error {
	if db == nil || strings.TrimSpace(token) == "" {
		return nil
	}
	return db.Where("token = ? AND user_id = ? AND organization_id = ?", strings.TrimSpace(token), userID, orgID).
		Delete(&MobileDevice{}).Error
}

func mirrorMobilePush(msg BroadcastMessage, log logf.Logger) {
	if !shouldMirrorToMobile(msg.Message.Type) {
		return
	}
	mobilePushState.RLock()
	service := mobilePushState.service
	mobilePushState.RUnlock()
	if service == nil || !service.enabled {
		return
	}
	go service.sendBroadcast(msg)
}

func shouldMirrorToMobile(messageType string) bool {
	switch messageType {
	case TypeCallTransferWaiting,
		TypeCallTransferConnected,
		TypeCallTransferCompleted,
		TypeCallTransferAbandoned,
		TypeCallTransferNoAnswer,
		TypeCallTransferReassigned,
		TypeCallEnded:
		return true
	default:
		return false
	}
}

func (s *mobilePushService) sendBroadcast(msg BroadcastMessage) {
	query := s.db.Where("organization_id = ?", msg.OrgID)
	if msg.UserID != uuid.Nil {
		query = query.Where("user_id = ?", msg.UserID)
	}
	var devices []MobileDevice
	if err := query.Find(&devices).Error; err != nil {
		s.log.Warn("Failed to query mobile push devices", "error", err, "org_id", msg.OrgID)
		return
	}
	if len(devices) == 0 {
		return
	}

	payloadJSON, err := json.Marshal(msg.Message.Payload)
	if err != nil {
		return
	}
	priority := "normal"
	ttl := "120s"
	if msg.Message.Type == TypeCallTransferWaiting {
		priority = "high"
		ttl = "45s"
	}

	for _, device := range devices {
		if err := s.sendToDevice(device, msg.Message.Type, string(payloadJSON), priority, ttl); err != nil {
			s.log.Warn("Failed to send mobile call push", "error", err, "device_id", device.ID, "event", msg.Message.Type)
		}
	}
}

func (s *mobilePushService) sendToDevice(device MobileDevice, event, payload, priority, ttl string) error {
	body := map[string]any{
		"message": map[string]any{
			"token": device.Token,
			"data": map[string]string{
				"event":   event,
				"payload": payload,
			},
			"android": map[string]any{
				"priority": priority,
				"ttl":      ttl,
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", s.projectID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	responseText := string(responseBody)
	if resp.StatusCode == http.StatusNotFound || strings.Contains(responseText, "UNREGISTERED") {
		_ = s.db.Where("id = ?", device.ID).Delete(&MobileDevice{}).Error
	}
	return fmt.Errorf("FCM returned %d: %s", resp.StatusCode, responseText)
}
