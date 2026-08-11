package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStartOpenAILoginValidatesDeviceResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s, %q", r.Method, r.Header.Get("Content-Type"))
		}
		fmt.Fprint(w, `{"device_auth_id":"device","user_code":"ABCD-EFGH","interval":"2"}`)
	}))
	defer server.Close()
	code, err := startOpenAILogin(context.Background(), server.Client(), server.URL)
	if err != nil || code.DeviceAuthID != "device" || code.Interval.Seconds() != 2 {
		t.Fatalf("code = %#v, error = %v", code, err)
	}
}

func TestStartOpenAILoginRejectsNonFiniteInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"device_auth_id":"device","user_code":"ABCD-EFGH","interval":"NaN"}`)
	}))
	defer server.Close()
	if _, err := startOpenAILogin(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("expected invalid interval error")
	}
}

func TestOpenAITokenExchangeExtractsAccount(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"account-1"}}`))
	access := "header." + payload + ".signature"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("form = %v, error = %v", r.Form, err)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"next","expires_in":3600}`, access)
	}))
	defer server.Close()
	credential, err := refreshOpenAIToken(context.Background(), server.Client(), server.URL, "old")
	if err != nil || credential.AccountID != "account-1" || credential.Refresh != "next" {
		t.Fatalf("credential = %#v, error = %v", credential, err)
	}
}

func TestAuthStorageUsesAtomHomeAndPrivateMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATOM_HOME", home)
	if err := saveOpenAIOAuth(OAuthCredential{Access: "access", Refresh: "refresh", Expires: 1}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("auth mode = %o", info.Mode().Perm())
	}
	auth, err := loadAuth()
	if err != nil || auth.OpenAIOAuth == nil || auth.OpenAIOAuth.Refresh != "refresh" {
		t.Fatalf("auth = %#v, error = %v", auth, err)
	}
}

func TestAuthMethodSwitchClearsPreviousOpenAIKind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATOM_HOME", home)
	if err := SaveAPIKey("openai", "key"); err != nil {
		t.Fatal(err)
	}
	if err := saveOpenAIOAuth(OAuthCredential{Access: "access", Refresh: "refresh", Expires: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := loadAuth()
	if err != nil || auth.OpenAIAPIKey != "" || auth.OpenAIOAuth == nil {
		t.Fatalf("auth after OAuth = %#v, error = %v", auth, err)
	}
	if err := SaveAPIKey("openai", "next-key"); err != nil {
		t.Fatal(err)
	}
	auth, err = loadAuth()
	if err != nil || auth.OpenAIAPIKey != "next-key" || auth.OpenAIOAuth != nil {
		t.Fatalf("auth after API key = %#v, error = %v", auth, err)
	}
}
