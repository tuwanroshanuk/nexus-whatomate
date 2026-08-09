package websocket

import "github.com/google/uuid"

// HasRegisteredMobileDevice reports whether a user has at least one native
// installation registered for call push. Rotation uses this in addition to
// live WebSocket presence so a logged-in Android app can ring while its
// process is backgrounded or killed.
//
// Invalid FCM registrations are removed by mobile_push.go when Firebase
// reports them as unregistered, so the presence of a row is the durable
// reachability signal for a signed-in native installation.
func HasRegisteredMobileDevice(orgID, userID uuid.UUID) bool {
	mobilePushState.RLock()
	service := mobilePushState.service
	mobilePushState.RUnlock()
	if service == nil || service.db == nil {
		return false
	}

	var count int64
	if err := service.db.Model(&MobileDevice{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Limit(1).
		Count(&count).Error; err != nil {
		service.log.Warn("Failed to check mobile agent reachability",
			"error", err,
			"org_id", orgID,
			"user_id", userID,
		)
		return false
	}
	return count > 0
}
