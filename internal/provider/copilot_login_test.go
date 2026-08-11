package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopilotLoginUsesCopilotChatClientID(t *testing.T) {
	if copilotClientID != "Iv1.b507a08c87ecfe98" {
		t.Fatalf("unexpected GitHub OAuth client ID: %q", copilotClientID)
	}
}

func TestStartCopilotLoginUsesDeviceFlowContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != copilotClientID || r.Form.Get("scope") != "read:user" || r.Header.Get("User-Agent") != "GitHubCopilotChat/0.35.0" {
			t.Fatalf("form=%v headers=%v error=%v", r.Form, r.Header, err)
		}
		fmt.Fprint(w, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","interval":2,"expires_in":900}`)
	}))
	defer server.Close()
	code, err := startCopilotLogin(context.Background(), server.Client(), server.URL)
	if err != nil || code.DeviceCode != "device" || code.Interval != 2 || code.ExpiresIn != 900 {
		t.Fatalf("code=%#v error=%v", code, err)
	}
}

func TestCopilotTokenExchangeAndProxyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer github-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"token":"tid=x;proxy-ep=proxy.business.githubcopilot.com;exp=99","expires_at":99}`)
	}))
	defer server.Close()
	access, err := exchangeCopilotToken(context.Background(), server.Client(), server.URL, "github-token")
	if err != nil {
		t.Fatal(err)
	}
	if got := copilotBaseURL(access.Token); got != "https://api.business.githubcopilot.com" {
		t.Fatalf("base URL = %q", got)
	}
}
