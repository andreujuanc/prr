package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// CopilotBaseURL is the API endpoint for GitHub Copilot chat completions.
	CopilotBaseURL = "https://api.githubcopilot.com"

	copilotDeviceCodeURL  = "https://github.com/login/device/code"
	copilotAccessTokenURL = "https://github.com/login/oauth/access_token"

	// Safety margin when polling to avoid clock skew.
	oauthPollingMargin = 3 * time.Second
)

// copilotHTTPClient is used for OAuth and token-validation requests.
// The default http.Client has no timeout, so a single hung GitHub
// API call would freeze the device-flow loop forever even when the
// caller has no context deadline.
var copilotHTTPClient = &http.Client{Timeout: 30 * time.Second}

// CopilotClientID returns the OAuth app client ID for the device flow.
// It reads from the PRR_COPILOT_CLIENT_ID environment variable.
func CopilotClientID() string {
	return os.Getenv("PRR_COPILOT_CLIENT_ID")
}

// CopilotDeviceAuth holds the response from the device code request.
type CopilotDeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"` // polling interval in seconds
}

// CopilotRequestDeviceCode initiates the OAuth device flow.
// The caller should display VerificationURI and UserCode to the user,
// then call CopilotPollForToken to wait for authorization.
func CopilotRequestDeviceCode(ctx context.Context) (*CopilotDeviceAuth, error) {
	clientID := CopilotClientID()
	if clientID == "" {
		return nil, fmt.Errorf("copilot: PRR_COPILOT_CLIENT_ID environment variable not set")
	}
	body := fmt.Sprintf(`{"client_id":%q,"scope":"read:user"}`, clientID)

	req, err := http.NewRequestWithContext(ctx, "POST", copilotDeviceCodeURL,
		strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("copilot: device code request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "prr")

	resp, err := copilotHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: device code request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot: device code request failed (HTTP %d): %s", resp.StatusCode, errBody)
	}

	var auth CopilotDeviceAuth
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return nil, fmt.Errorf("copilot: failed to parse device code response: %w", err)
	}
	return &auth, nil
}

// CopilotPollForToken polls GitHub for the OAuth access token after the user
// has authorized the device. It blocks until authorization succeeds, the context
// is cancelled, or an unrecoverable error occurs.
func CopilotPollForToken(ctx context.Context, deviceCode string, interval int) (string, error) {
	pollInterval := time.Duration(interval)*time.Second + oauthPollingMargin

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		body := fmt.Sprintf(`{"client_id":%q,"device_code":%q,"grant_type":"urn:ietf:params:oauth:grant-type:device_code"}`,
			CopilotClientID(), deviceCode)

		req, err := http.NewRequestWithContext(ctx, "POST", copilotAccessTokenURL,
			strings.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("copilot: token poll: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "prr")

		resp, err := copilotHTTPClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("copilot: token poll: %w", err)
		}

		var result struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		// Without this check a malformed response leaves result
		// zero-valued, the switch below falls through to the empty
		// case, and the poll loop spins forever pretending GitHub
		// answered "authorization_pending".
		if decodeErr != nil {
			return "", fmt.Errorf("copilot: token poll: decode response: %w", decodeErr)
		}

		if result.AccessToken != "" {
			return result.AccessToken, nil
		}

		switch result.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			// RFC 8628: add 5 seconds to current interval
			newInterval := interval + 5
			if result.Interval > 0 {
				newInterval = result.Interval
			}
			pollInterval = time.Duration(newInterval)*time.Second + oauthPollingMargin
			continue
		case "":
			continue
		default:
			return "", fmt.Errorf("copilot: authorization failed: %s", result.Error)
		}
	}
}

// CopilotValidateToken checks if a Copilot OAuth token is valid by fetching /models.
func CopilotValidateToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", CopilotBaseURL+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "prr")

	resp, err := copilotHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot: validation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot: token validation failed (HTTP %d): %s", resp.StatusCode, errBody)
	}
	return nil
}
