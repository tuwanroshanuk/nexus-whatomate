package websocket

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMobileMessageRecipient(t *testing.T) {
	assigned := uuid.New()
	recipient, ok := mobileMessageRecipient(map[string]any{
		"direction":        "incoming",
		"assigned_user_id": assigned.String(),
	})
	require.True(t, ok)
	assert.Equal(t, assigned, recipient)
}

func TestMobileMessageRecipientSkipsNoise(t *testing.T) {
	assigned := uuid.New().String()
	for _, payload := range []any{
		map[string]any{"direction": "outgoing", "assigned_user_id": assigned},
		map[string]any{"direction": "incoming", "assigned_user_id": ""},
		map[string]any{"direction": "incoming"},
		"invalid",
	} {
		_, ok := mobileMessageRecipient(payload)
		assert.False(t, ok)
	}
}

func TestNewMessagesAreMirroredToMobile(t *testing.T) {
	assert.True(t, shouldMirrorToMobile(TypeNewMessage))
}
