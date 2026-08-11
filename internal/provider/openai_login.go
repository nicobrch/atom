package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	openAIClientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIDeviceUserCodeURL     = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	openAIDeviceTokenURL        = "https://auth.openai.com/api/accounts/deviceauth/token"
	openAIDeviceVerificationURL = "https://auth.openai.com/codex/device"
	openAIDeviceRedirectURI     = "https://auth.openai.com/deviceauth/callback"
	openAITokenURL              = "https://auth.openai.com/oauth/token"
)

type OpenAIDeviceCode struct {
	DeviceAuthID string
	UserCode     string
	Interval     time.Duration
}

func StartOpenAILogin(ctx context.Context) (OpenAIDeviceCode, error) {
	return startOpenAILogin(ctx, http.DefaultClient, openAIDeviceUserCodeURL)
}

func startOpenAILogin(ctx context.Context, client *http.Client, endpoint string) (OpenAIDeviceCode, error) {
	body, _ := json.Marshal(map[string]string{"client_id": openAIClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OpenAIDeviceCode{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return OpenAIDeviceCode{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return OpenAIDeviceCode{}, fmt.Errorf("start OpenAI login: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var value struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return OpenAIDeviceCode{}, err
	}
	var seconds float64
	if err := json.Unmarshal(value.Interval, &seconds); err != nil {
		var text string
		if json.Unmarshal(value.Interval, &text) != nil {
			return OpenAIDeviceCode{}, fmt.Errorf("invalid OpenAI device login interval")
		}
		var parseErr error
		seconds, parseErr = strconv.ParseFloat(strings.TrimSpace(text), 64)
		if parseErr != nil {
			return OpenAIDeviceCode{}, fmt.Errorf("invalid OpenAI device login interval")
		}
	}
	if value.DeviceAuthID == "" || value.UserCode == "" || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return OpenAIDeviceCode{}, fmt.Errorf("invalid OpenAI device login response")
	}
	return OpenAIDeviceCode{DeviceAuthID: value.DeviceAuthID, UserCode: value.UserCode, Interval: time.Duration(seconds * float64(time.Second))}, nil
}

func FinishOpenAILogin(ctx context.Context, code OpenAIDeviceCode) error {
	credential, err := finishOpenAILogin(ctx, http.DefaultClient, code, openAIDeviceTokenURL, openAITokenURL)
	if err != nil {
		return err
	}
	return saveOpenAIOAuth(credential)
}

func finishOpenAILogin(ctx context.Context, client *http.Client, code OpenAIDeviceCode, deviceEndpoint, tokenEndpoint string) (OAuthCredential, error) {
	interval := code.Interval
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return OAuthCredential{}, ctx.Err()
		case <-deadline.C:
			return OAuthCredential{}, fmt.Errorf("OpenAI device login timed out")
		case <-time.After(interval):
		}
		body, _ := json.Marshal(map[string]string{"device_auth_id": code.DeviceAuthID, "user_code": code.UserCode})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceEndpoint, bytes.NewReader(body))
		if err != nil {
			return OAuthCredential{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return OAuthCredential{}, err
		}
		var result struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
			Error             any    `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			continue
		}
		if decodeErr != nil {
			return OAuthCredential{}, decodeErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if strings.Contains(fmt.Sprint(result.Error), "slow_down") {
				interval += 5 * time.Second
				continue
			}
			return OAuthCredential{}, fmt.Errorf("OpenAI device login failed: HTTP %d: %v", resp.StatusCode, result.Error)
		}
		if result.AuthorizationCode == "" || result.CodeVerifier == "" {
			return OAuthCredential{}, fmt.Errorf("invalid OpenAI device token response")
		}
		return exchangeOpenAIToken(ctx, client, tokenEndpoint, map[string]string{
			"grant_type": "authorization_code", "client_id": openAIClientID, "code": result.AuthorizationCode,
			"code_verifier": result.CodeVerifier, "redirect_uri": openAIDeviceRedirectURI,
		})
	}
}

func refreshOpenAIToken(ctx context.Context, client *http.Client, endpoint, refresh string) (OAuthCredential, error) {
	return exchangeOpenAIToken(ctx, client, endpoint, map[string]string{
		"grant_type": "refresh_token", "refresh_token": refresh, "client_id": openAIClientID,
	})
}

func exchangeOpenAIToken(ctx context.Context, client *http.Client, endpoint string, values map[string]string) (OAuthCredential, error) {
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthCredential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return OAuthCredential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return OAuthCredential{}, fmt.Errorf("OpenAI token exchange failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var value struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return OAuthCredential{}, err
	}
	if value.Access == "" || value.Refresh == "" || value.Expires <= 0 {
		return OAuthCredential{}, fmt.Errorf("invalid OpenAI token response")
	}
	accountID, err := openAIAccountID(value.Access)
	if err != nil {
		return OAuthCredential{}, err
	}
	return OAuthCredential{Access: value.Access, Refresh: value.Refresh, Expires: time.Now().Add(time.Duration(value.Expires) * time.Second).UnixMilli(), AccountID: accountID}, nil
}

func openAIAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid OpenAI access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode OpenAI access token: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode OpenAI access token claims: %w", err)
	}
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(claims["https://api.openai.com/auth"], &auth); err != nil || auth.AccountID == "" {
		return "", fmt.Errorf("OpenAI access token has no ChatGPT account ID")
	}
	return auth.AccountID, nil
}
