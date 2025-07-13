package kite

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"io"
	"net/http"
	"time"

	"github.com/ananthakumaran/paisa/internal/model"
)

// GetValidAccessToken returns a valid access token from the database. If the existing access token is expired, it will be refreshed.
func GetValidAccessToken(db *gorm.DB, apiKey string) (string, error) {
	// Get the current authentication data from the database
	auth, err := model.GetLatestAuth(db)
	if err != nil {
		return "", fmt.Errorf("failed to get stored authentication data: %w", err)
	}

	// If we already have an access token, check whether it is expired.
	if auth != nil && auth.AccessToken != "" && !checkIfAccessTokenIsExpired(apiKey, auth.AccessToken) {
		return auth.AccessToken, nil
	}

	if auth == nil || auth.RequestToken == "" {
		// No request token found in database, attempt to login and store the request token.
		log.Info("No request token found in database, attempt to login and store the request token.")
		err = LoginAndStoreToken(db)
		if err != nil {
			return "", fmt.Errorf("failed to login and store token: %w", err)
		}

		auth, err = model.GetLatestAuth(db)
		if err != nil {
			return "", fmt.Errorf("failed to get latest auth: %w", err)
		}
	}

	if auth == nil || auth.RequestToken == "" {
		// Unexpected. Since, a login flow should have created a request token.
		return "", fmt.Errorf("Unexpected. Login flow should have created a request token.")
	}

	// Try to get access token with retry logic
	accessToken, err := getAccessTokenFromRequestTokenWithRetry(db, auth.RequestToken)
	if err != nil {
		return "", fmt.Errorf("failed to get access token after retry: %w", err)
	}

	err = model.UpdateAccessToken(db, accessToken)
	if err != nil {
		return "", fmt.Errorf("failed to update access token in database: %w", err)
	}

	return accessToken, nil
}

func checkIfAccessTokenIsExpired(apiKey string, accessToken string) bool {
	// Make request to get user profile using the access token
	url := "https://api.kite.trade/user/profile"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Errorf("Failed to create request: %v", err)
		return true // Assume expired if we can't create request
	}

	// Add authentication headers as per Kite API specification
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", apiKey, accessToken))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Failed to make request: %v", err)
		return true
	}
	defer resp.Body.Close()

	// If we get a 403 status, check if it's a token expiration error
	if resp.StatusCode == http.StatusForbidden {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Errorf("Failed to read response body: %v", err)
			return true
		}

		var errorResponse struct {
			Status    string `json:"status"`
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		}

		err = json.Unmarshal(body, &errorResponse)
		if err != nil {
			log.Errorf("Failed to parse error response: %v", err)
			return true // Assume expired if we can't parse response
		}

		// Check if the error is specifically a TokenException
		if errorResponse.Status == "error" && errorResponse.ErrorType == "TokenException" {
			log.Info("Access token is expired (TokenException)")
			return true
		}
	}

	// If we get a successful response (200), the token is valid
	if resp.StatusCode == http.StatusOK {
		log.Debug("Access token is valid")
		return false
	}

	// For any other status code, assume the token is expired
	log.Warnf("Unexpected status code %d, assuming token is expired", resp.StatusCode)
	return true
}

// getAccessTokenFromRequestTokenWithRetry attempts to get an access token with automatic retry logic
func getAccessTokenFromRequestTokenWithRetry(db *gorm.DB, requestToken string) (string, error) {
	const maxRetries = 2

	for attempt := 0; attempt <= maxRetries; attempt++ {
		accessToken, err := FetchAccessTokenFromRequestToken(requestToken)
		if err == nil {
			return accessToken, nil
		}

		// If this is the last attempt, return the error
		if attempt == maxRetries {
			return "", fmt.Errorf("failed to get access token after %d attempts: %w", maxRetries+1, err)
		}

		log.Infof("Attempt %d failed to fetch access token, starting new login flow...", attempt+1)

		// Start a new login flow to get a fresh request token
		err = LoginAndStoreToken(db)
		if err != nil {
			return "", fmt.Errorf("failed to login and store token on attempt %d: %w", attempt+1, err)
		}

		// Get the updated auth data with new request token
		auth, err := model.GetLatestAuth(db)
		if err != nil {
			return "", fmt.Errorf("failed to get latest auth after login on attempt %d: %w", attempt+1, err)
		}

		if auth == nil || auth.RequestToken == "" {
			return "", fmt.Errorf("no request token found after login on attempt %d", attempt+1)
		}

		// Update request token for next iteration
		requestToken = auth.RequestToken
	}

	// This should never be reached due to the return in the loop
	return "", fmt.Errorf("unexpected: retry loop completed without result")
}
