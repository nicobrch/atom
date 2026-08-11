package provider

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/nicobrch/atom/internal/agent"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
}

var (
	bearerToken = regexp.MustCompile(`(?i)bearer\s+[^\s,;]+`)
	apiKey      = regexp.MustCompile(`\b(?:sk|sess)-[A-Za-z0-9_-]+\b`)
)

func providerFailure(stage string, err error) *agent.ProviderError {
	return &agent.ProviderError{Stage: stage, Message: sanitizeDiagnostic(err.Error())}
}

func httpFailure(stage string, statusCode int, requestID string, body []byte) *agent.ProviderError {
	detail := apiError{}
	_ = json.Unmarshal(body, &struct {
		Error *apiError `json:"error"`
	}{Error: &detail})
	if detail.Message == "" {
		// Some compatible providers return the error fields at the top level.
		_ = json.Unmarshal(body, &detail)
	}
	if detail.Message == "" {
		detail.Message = "provider returned a non-success HTTP response"
	}
	return &agent.ProviderError{
		Stage: stage, StatusCode: statusCode, RequestID: requestID,
		Code: detail.Code, Type: detail.Type, Param: detail.Param, Message: sanitizeDiagnostic(detail.Message),
	}
}

func responseFailure(stage, responseID string, detail apiError) *agent.ProviderError {
	if detail.Message == "" {
		detail.Message = "provider ended the streamed response with an error"
	}
	return &agent.ProviderError{
		Stage: stage, ResponseID: responseID, Code: detail.Code, Type: detail.Type,
		Param: detail.Param, Message: sanitizeDiagnostic(detail.Message),
	}
}

func enrichFailure(err error, statusCode int, requestID string) error {
	var failure *agent.ProviderError
	if errors.As(err, &failure) {
		if failure.StatusCode == 0 {
			failure.StatusCode = statusCode
		}
		if failure.RequestID == "" {
			failure.RequestID = requestID
		}
	}
	return err
}

func responseRequestID(headers map[string][]string) string {
	for _, key := range []string{"X-Request-Id", "X-Request-ID", "Request-Id", "Request-ID"} {
		for header, values := range headers {
			if strings.EqualFold(header, key) && len(values) > 0 && values[0] != "" {
				return values[0]
			}
		}
	}
	return ""
}

func sanitizeDiagnostic(message string) string {
	message = bearerToken.ReplaceAllString(message, "Bearer [REDACTED]")
	message = apiKey.ReplaceAllString(message, "[REDACTED]")
	message = strings.Join(strings.Fields(message), " ")
	const maxLength = 1024
	if len(message) > maxLength {
		return message[:maxLength] + "…"
	}
	return message
}
