package agent

import (
	"encoding/json"
	"testing"
)

func TestNewEventKeepsWireEnvelopeMinimal(t *testing.T) {
	event, err := NewEvent("event-1", "run-1", EventMessageDelta, map[string]string{"content": "回答"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || containsJSONField(payload, "mode") {
		t.Fatalf("event payload = %s, must not contain derived mode field", payload)
	}
}

func containsJSONField(payload []byte, field string) bool {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return false
	}
	_, ok := values[field]
	return ok
}
