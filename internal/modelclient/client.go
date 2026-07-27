package modelclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ConnectionChecker verifies that an API endpoint accepts the configured key.
type ConnectionChecker interface {
	Check(context.Context, string, string) error
}

type HTTPConnectionChecker struct {
	client       *http.Client
	allowedHosts map[string]struct{}
}

func NewHTTPConnectionChecker(client *http.Client, allowedHosts []string) *HTTPConnectionChecker {
	if client == nil {
		client = http.DefaultClient
	}
	noRedirectClient := *client
	noRedirectClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	trustedHosts := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		trustedHosts[strings.ToLower(host)] = struct{}{}
	}
	return &HTTPConnectionChecker{client: &noRedirectClient, allowedHosts: trustedHosts}
}

func (c *HTTPConnectionChecker) Check(ctx context.Context, baseURL, apiKey string) error {
	endpoint, err := modelsEndpoint(baseURL, c.allowedHosts)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)

	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("request models endpoint: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("models endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func modelsEndpoint(baseURL string, allowedHosts map[string]struct{}) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model provider base URL")
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("model provider base URL must use HTTPS")
	}
	if _, allowed := allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
		return "", fmt.Errorf("model provider base URL host is not allowed")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawQuery = ""
	return parsed.String(), nil
}
