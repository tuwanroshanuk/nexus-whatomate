package calling

import (
	"encoding/json"
	"testing"
)

func TestFlattenJSONVariables(t *testing.T) {
	raw := `{"profile":{"name":"Sputnik Kurunegala","isLocked":false},"projects":[{"name":"Michibatha Cafe","service":{"name":"Nexus Cloud POS"},"status":"cancelled"}]}`
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}

	vars := map[string]string{}
	flattenJSONVariables("accountInfo", parsed, vars)

	expected := map[string]string{
		"accountInfo.profile.name":            "Sputnik Kurunegala",
		"accountInfo.profile.isLocked":        "false",
		"accountInfo.projects.0.name":         "Michibatha Cafe",
		"accountInfo.projects.0.service.name": "Nexus Cloud POS",
		"accountInfo.projects.0.status":       "cancelled",
	}
	for key, want := range expected {
		if got := vars[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
