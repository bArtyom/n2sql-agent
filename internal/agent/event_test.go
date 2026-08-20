package agent

import "testing"

func TestNewEventAssignsDeerFlowStreamMode(t *testing.T) {
	tests := []struct {
		name      string
		eventType EventType
		mode      StreamMode
	}{
		{name: "state", eventType: EventRunStarted, mode: StreamModeValues},
		{name: "model messages", eventType: EventMessageDelta, mode: StreamModeMessages},
		{name: "application progress", eventType: EventToolFinished, mode: StreamModeCustom},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := NewEvent("event-1", "run-1", test.eventType, nil)
			if err != nil {
				t.Fatal(err)
			}
			if event.Mode != test.mode {
				t.Fatalf("mode = %q, want %q", event.Mode, test.mode)
			}
		})
	}
}
