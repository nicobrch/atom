package provider

import "testing"

func TestCopilotLoginUsesAtomOwnedClientID(t *testing.T) {
	if copilotClientID != "Ov23liymK8r0F637IA3P" {
		t.Fatalf("unexpected GitHub OAuth client ID: %q", copilotClientID)
	}
}
