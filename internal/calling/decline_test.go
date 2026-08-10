package calling

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

func TestTriedAgentUUIDsDeduplicatesAndSkipsInvalid(t *testing.T) {
	first := uuid.New()
	second := uuid.New()
	values := models.JSONBArray{first.String(), "not-a-uuid", second.String(), first.String()}

	got := triedAgentUUIDs(values)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique valid agents, got %d", len(got))
	}
	if got[0] != first || got[1] != second {
		t.Fatalf("unexpected agent order: %v", got)
	}
}

func TestContainsAgent(t *testing.T) {
	first := uuid.New()
	second := uuid.New()
	if !containsAgent([]uuid.UUID{first}, first) {
		t.Fatal("expected first agent to be found")
	}
	if containsAgent([]uuid.UUID{first}, second) {
		t.Fatal("did not expect second agent to be found")
	}
}
