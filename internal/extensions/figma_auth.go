package extensions

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const figmaMeEndpoint = "https://api.figma.com/v1/me"

// ValidateFigmaAuthorization confirms that the selected credential is accepted
// by Figma. GET /v1/me requires current_user:read, which also gives CodingTo a
// deterministic success signal instead of treating any non-empty string as an
// authorized account.
func ValidateFigmaAuthorization(ctx context.Context, authorization FigmaAuthorization) error {
	client := &http.Client{Timeout: 15 * time.Second}
	return validateFigmaAuthorization(ctx, client, figmaMeEndpoint, authorization)
}

func validateFigmaAuthorization(ctx context.Context, client *http.Client, endpoint string, authorization FigmaAuthorization) error {
	token := strings.TrimSpace(authorization.Token)
	if token == "" {
		return fmt.Errorf("Figma token is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Figma authorization request: %w", err)
	}
	if authorization.TokenType == "oauth" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("X-Figma-Token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to Figma: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Figma rejected the token; check that it is valid, unexpired, and includes current_user:read")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Figma authorization check returned %s", resp.Status)
	}
	return nil
}
