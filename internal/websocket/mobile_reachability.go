package websocket

import "github.com/google/uuid"

// HasRegisteredMobileDevice reports whether a user has at least one native
// installation that the server can currently reach for call push. Rotation
// uses this in addition to live WebSocket presence so a logged-in Android app
// can ring while its process is backgrounded or killed.
//
// The FCM service must be enabled. Merely retaining a device row is not enough:
// selecting a background-only agent while Firebase credentials are missing
// would make the rotation wait on a device that the server cannot ring.
// Invalid FCM registrations are removed by mobile_push.go when Firebase reports
// them as unregistered.
func HasRegisteredMobileDevice(orgID, userID uuid.UUID) bool {
	mobilePushState.RLock()
	service := mobilePushState.service
	mobilePushState.RUnlock()
	if service == nil || service.db == nil || !service.enabled {
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
