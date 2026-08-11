package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// copilotClientID belongs to Atom's GitHub OAuth app. It is public client
// metadata, not a client secret; GitHub Device Flow only uses the client ID.
const copilotClientID = "Ov23liymK8r0F637IA3P"

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
		delay := code.Interval
		if result.Error == "slow_down" {
			delay += 5
		}
		time.Sleep(time.Duration(delay) * time.Second)
	}
}
