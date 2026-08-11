package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Public GitHub Copilot Chat client metadata. Device Flow uses no client
// secret; this client is required for GitHub's Copilot token exchange.
const copilotClientID = "Iv1.b507a08c87ecfe98"

type CopilotDeviceCode struct {
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func StartCopilotLogin(ctx context.Context) (CopilotDeviceCode, error) {
	return startCopilotLogin(ctx, http.DefaultClient, "https://github.com/login/device/code")
}

func startCopilotLogin(ctx context.Context, client *http.Client, endpoint string) (CopilotDeviceCode, error) {
	form := url.Values{"client_id": {copilotClientID}, "scope": {"read:user"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return CopilotDeviceCode{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	resp, err := client.Do(req)
	if err != nil {
		return CopilotDeviceCode{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CopilotDeviceCode{}, fmt.Errorf("start Copilot login: %s", resp.Status)
	}
	var code CopilotDeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&code); err != nil {
		return CopilotDeviceCode{}, err
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" || code.ExpiresIn < 1 {
		return CopilotDeviceCode{}, fmt.Errorf("invalid Copilot device response")
	}
	verificationURL, err := url.Parse(code.VerificationURI)
	if err != nil || verificationURL.Host == "" || verificationURL.Scheme != "https" && verificationURL.Scheme != "http" {
		return CopilotDeviceCode{}, fmt.Errorf("invalid Copilot verification URI")
	}
	if code.Interval < 1 {
		code.Interval = 5
	}
	return code, nil
}

func FinishCopilotLogin(ctx context.Context, code CopilotDeviceCode) error {
	interval := time.Duration(code.Interval) * time.Second
	deadline := time.NewTimer(time.Duration(code.ExpiresIn) * time.Second)
	defer deadline.Stop()
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return fmt.Errorf("Copilot login timed out")
		case <-timer.C:
		}
		form := url.Values{"client_id": {copilotClientID}, "device_code": {code.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		var result struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if result.AccessToken != "" {
			return SaveCopilotToken(result.AccessToken)
		}
		if result.Error != "authorization_pending" && result.Error != "slow_down" {
			return fmt.Errorf("Copilot login failed: %s", result.Error)
		}
		if result.Error == "slow_down" {
			interval += 5 * time.Second
		}
	}
}
