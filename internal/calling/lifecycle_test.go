package calling

import "testing"

func TestIsCurrentSessionUsesObjectIdentity(t *testing.T) {
	oldSession := &CallSession{ID: "same-call-id"}
	currentSession := &CallSession{ID: "same-call-id"}
	m := &Manager{sessions: map[string]*CallSession{"same-call-id": currentSession}}

	if m.isCurrentSession(oldSession) {
		t.Fatal("stale session with reused call ID must not be current")
	}
	if !m.isCurrentSession(currentSession) {
		t.Fatal("registered session should be current")
	}
}
