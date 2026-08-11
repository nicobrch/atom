package provider

import "testing"

func TestCopilotLoginUsesCopilotClientID(t *testing.T) {
	if copilotClientID != "Iv1.b507a08c87ecfe98" {
		t.Fatalf("unexpected GitHub OAuth client ID: %q", copilotClientID)
	}
}
