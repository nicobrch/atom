package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const copilotClientID = "Iv1.b507a08c87ecfe98"

var copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

var copilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.26.7",
	"Editor-Version":         "vscode/1.99.3",
	"Editor-Plugin-Version":  "copilot-chat/0.26.7",
	"Copilot-Integration-Id": "vscode-chat",
}

type CopilotDeviceCode struct {
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
}

func StartCopilotLogin() (CopilotDeviceCode, error) {
	b, _ := json.Marshal(map[string]string{"client_id": copilotClientID, "scope": "read:user"})
	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/device/code", bytes.NewReader(b))
	if err != nil {
		return CopilotDeviceCode{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", copilotHeaders["User-Agent"])
	resp, err := http.DefaultClient.Do(req)
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
	if code.DeviceCode == "" || code.UserCode == "" {
		return CopilotDeviceCode{}, fmt.Errorf("invalid Copilot device response")
	}
	if code.Interval < 1 {
		code.Interval = 5
	}
	return code, nil
}

func FinishCopilotLogin(code CopilotDeviceCode) error {
	for {
		b, _ := json.Marshal(map[string]string{"client_id": copilotClientID, "device_code": code.DeviceCode, "grant_type": "urn:ietf:params:oauth:grant-type:device_code"})
		req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", copilotHeaders["User-Agent"])
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
			return SaveCopilotOAuthToken(result.AccessToken)
		}
		if result.Error != "authorization_pending" && result.Error != "slow_down" {
			return fmt.Errorf("Copilot login failed: %s", result.Error)
		}
		delay := code.Interval
		if result.Error == "slow_down" {
			delay += 5
		}
		time.Sleep(time.Duration(delay) * time.Second)
	}
}

func copilotAPIToken(oauthToken string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	for name, value := range copilotHeaders {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return "", "", fmt.Errorf("refresh Copilot token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode Copilot token: %w", err)
	}
	if result.Token == "" {
		return "", "", fmt.Errorf("refresh Copilot token: empty token")
	}
	return result.Token, result.Endpoints.API, nil
}
