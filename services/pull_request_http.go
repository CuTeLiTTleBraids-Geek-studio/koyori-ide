package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	pullRequestDefaultTimeout = 15 * time.Second
	pullRequestMaxJSONBody    = int64(2 << 20)
	pullRequestMaxDiffBody    = int64(5 << 20)
)

func (s *PullRequestService) doJSON(repository PullRequestRepository, token, method, path string, query url.Values, body, output any) (http.Header, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode provider request: %w", err)
		}
		if len(encoded) > 1<<20 {
			return nil, fmt.Errorf("provider request body exceeds size limit")
		}
		payload = bytes.NewReader(encoded)
	}
	response, err := s.execute(repository, token, method, path, query, payload, "application/json")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := readPullRequestResponseBody(response.Body, pullRequestMaxJSONBody)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, sanitizePullRequestHTTPError(response.StatusCode)
	}
	if output != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return nil, fmt.Errorf("provider returned an invalid response")
		}
	}
	return response.Header.Clone(), nil
}

func (s *PullRequestService) doRaw(repository PullRequestRepository, token, method, path, accept string) (string, error) {
	response, err := s.execute(repository, token, method, path, nil, nil, accept)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, err := readPullRequestResponseBody(response.Body, pullRequestMaxDiffBody)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", sanitizePullRequestHTTPError(response.StatusCode)
	}
	return string(data), nil
}

func (s *PullRequestService) execute(repository PullRequestRepository, token, method, path string, query url.Values, body io.Reader, accept string) (*http.Response, error) {
	requestURL, err := buildPullRequestAPIURL(repository, path, query)
	if err != nil {
		return nil, err
	}
	if err := validatePullRequestAPIURL(repository, requestURL); err != nil {
		return nil, err
	}
	request, err := http.NewRequest(method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create provider request")
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "koyori-ide-pull-requests")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if repository.Provider == PullRequestProviderGitHub {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	} else {
		request.Header.Set("PRIVATE-TOKEN", token)
	}
	s.mu.RLock()
	transport := s.transport
	s.mu.RUnlock()
	if transport == nil {
		transport = NewSSRFSafeTransport()
	}
	client := &http.Client{
		Timeout:   pullRequestDefaultTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("redirects are not allowed")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "redirect") {
			return nil, fmt.Errorf("provider redirect rejected")
		}
		return nil, fmt.Errorf("provider request failed")
	}
	return response, nil
}

func buildPullRequestAPIURL(repository PullRequestRepository, path string, query url.Values) (*url.URL, error) {
	base := strings.TrimRight(repository.APIBaseURL, "/")
	parsed, err := url.Parse(base + path)
	if err != nil {
		return nil, fmt.Errorf("build provider URL")
	}
	if query != nil {
		parsed.RawQuery = query.Encode()
	}
	return parsed, nil
}

func validatePullRequestAPIURL(repository PullRequestRepository, requestURL *url.URL) error {
	if requestURL == nil || requestURL.Scheme != "https" || requestURL.Hostname() == "" || requestURL.User != nil {
		return fmt.Errorf("provider URL must be HTTPS without credentials")
	}
	host := strings.ToLower(requestURL.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("provider URL targets a private host")
	}
	if ip := net.ParseIP(host); ip != nil && isPrivateHost(ip) {
		return fmt.Errorf("provider URL targets a private address")
	}
	switch repository.Provider {
	case PullRequestProviderGitHub:
		if host != "api.github.com" && host != "github.com" {
			return fmt.Errorf("provider URL host is not allowed")
		}
	case PullRequestProviderGitLab:
		base, err := url.Parse(repository.APIBaseURL)
		if err != nil || !strings.EqualFold(requestURL.Host, base.Host) {
			return fmt.Errorf("provider URL host is not allowed")
		}
	default:
		return fmt.Errorf("unsupported pull request provider")
	}
	return nil
}

func readPullRequestResponseBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read provider response")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("provider response exceeds size limit")
	}
	return data, nil
}

func sanitizePullRequestHTTPError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("provider authentication failed")
	case http.StatusForbidden:
		return fmt.Errorf("provider access denied or rate limited")
	case http.StatusNotFound:
		return fmt.Errorf("pull request resource not found")
	case http.StatusConflict:
		return fmt.Errorf("provider rejected the request due to a conflict")
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("provider rejected the request as invalid")
	case http.StatusTooManyRequests:
		return fmt.Errorf("provider rate limit exceeded")
	default:
		return fmt.Errorf("provider request failed with status %d", status)
	}
}
