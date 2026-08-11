package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
)

type pullRequestContextBody struct {
	ctx  context.Context
	data []byte
}

func (b *pullRequestContextBody) Read(destination []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, fmt.Errorf("request context canceled before response body was read")
	default:
	}
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(destination, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (*pullRequestContextBody) Close() error { return nil }

type pullRequestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pullRequestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type pullRequestSecretStub struct {
	mu        sync.Mutex
	token     string
	err       error
	configIDs []string
}

func (s *pullRequestSecretStub) getAPIKeyForConfig(configID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configIDs = append(s.configIDs, configID)
	return s.token, s.err
}

func createPullRequestTestRepo(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repository: %v", err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteURL}})
	if err != nil {
		t.Fatalf("create remote: %v", err)
	}
	return dir
}

func pullRequestHTTPResponse(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestPullRequestServiceDetectRepository(t *testing.T) {
	tests := []struct {
		name       string
		remoteURL  string
		gitlabBase string
		provider   string
		host       string
		owner      string
		repository string
	}{
		{
			name:       "github scp remote",
			remoteURL:  "git@github.com:acme/widgets.git",
			provider:   PullRequestProviderGitHub,
			host:       "github.com",
			owner:      "acme",
			repository: "widgets",
		},
		{
			name:       "gitlab nested project",
			remoteURL:  "https://gitlab.example.com/group/platform/widgets.git",
			gitlabBase: "https://gitlab.example.com",
			provider:   PullRequestProviderGitLab,
			host:       "gitlab.example.com",
			owner:      "group/platform",
			repository: "widgets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath := createPullRequestTestRepo(t, tt.remoteURL)
			service := NewPullRequestService(nil, &pullRequestSecretStub{})
			repo, err := service.DetectRepository(PullRequestConnection{
				RepoPath:      repoPath,
				GitLabBaseURL: tt.gitlabBase,
			})
			if err != nil {
				t.Fatalf("DetectRepository: %v", err)
			}
			if repo.Provider != tt.provider || repo.Host != tt.host || repo.Owner != tt.owner || repo.Name != tt.repository {
				t.Fatalf("unexpected repository: %#v", repo)
			}
		})
	}
}

func TestPullRequestServiceGitHubListUsesSecretAndCapsPagination(t *testing.T) {
	repoPath := createPullRequestTestRepo(t, "git@github.com:acme/widgets.git")
	secrets := &pullRequestSecretStub{token: "github-secret-token"}
	var pages []string
	service := NewPullRequestService(nil, secrets)
	service.setHTTPTransport(pullRequestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer github-secret-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		if req.Header.Get("PRIVATE-TOKEN") != "" {
			t.Fatal("GitHub request must not use GitLab authentication")
		}
		if req.URL.Host != "api.github.com" || req.URL.Path != "/repos/acme/widgets/pulls" {
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		pages = append(pages, req.URL.Query().Get("page"))
		page := req.URL.Query().Get("page")
		body := fmt.Sprintf(`[{"number":%s,"title":"PR %s","state":"open","user":{"login":"octo"},"head":{"ref":"feature"},"base":{"ref":"main"}}]`, page, page)
		headers := map[string]string{}
		if page == "1" || page == "2" {
			next := "2"
			if page == "2" {
				next = "3"
			}
			headers["Link"] = fmt.Sprintf(`<https://api.github.com/repos/acme/widgets/pulls?state=open&per_page=1&page=%s>; rel="next"`, next)
		}
		return pullRequestHTTPResponse(http.StatusOK, body, headers), nil
	}))

	result, err := service.ListPullRequests(PullRequestListRequest{
		Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "github-config"},
		State:      "open",
		PerPage:    1,
		MaxPages:   2,
	})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if got, want := strings.Join(pages, ","), "1,2"; got != want {
		t.Fatalf("pages = %s, want %s", got, want)
	}
	if len(result.Items) != 2 || !result.Truncated {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(secrets.configIDs) != 1 || secrets.configIDs[0] != "github-config" {
		t.Fatalf("secret resolver calls: %#v", secrets.configIDs)
	}
}

func TestPullRequestServiceGitLabListAndCreate(t *testing.T) {
	repoPath := createPullRequestTestRepo(t, "ssh://git@gitlab.example.com/group/platform/widgets.git")
	secrets := &pullRequestSecretStub{token: "gitlab-secret-token"}
	var sawList, sawCreate bool
	service := NewPullRequestService(nil, secrets)
	service.setHTTPTransport(pullRequestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("PRIVATE-TOKEN") != "gitlab-secret-token" {
			t.Fatalf("missing GitLab token: %#v", req.Header)
		}
		if req.URL.Host != "gitlab.example.com" || !strings.Contains(req.URL.EscapedPath(), "group%2Fplatform%2Fwidgets") {
			t.Fatalf("unexpected GitLab URL: %s", req.URL)
		}
		switch req.Method {
		case http.MethodGet:
			sawList = true
			if req.URL.Query().Get("state") != "opened" {
				t.Fatalf("GitLab state was not mapped: %s", req.URL.RawQuery)
			}
			return pullRequestHTTPResponse(http.StatusOK, `[{"iid":4,"title":"Fix","state":"opened","author":{"username":"sam"},"source_branch":"fix","target_branch":"main"}]`, map[string]string{}), nil
		case http.MethodPost:
			sawCreate = true
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			body := string(data)
			for _, expected := range []string{`"source_branch":"feature"`, `"target_branch":"main"`, `"title":"Ship it"`} {
				if !strings.Contains(body, expected) {
					t.Fatalf("create body %q missing %q", body, expected)
				}
			}
			return pullRequestHTTPResponse(http.StatusCreated, `{"iid":9,"title":"Ship it","state":"opened","source_branch":"feature","target_branch":"main"}`, nil), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", req.Method)
		}
	}))
	connection := PullRequestConnection{
		RepoPath:      repoPath,
		ConfigID:      "gitlab-config",
		GitLabBaseURL: "https://gitlab.example.com",
	}
	listed, err := service.ListPullRequests(PullRequestListRequest{Connection: connection, State: "open"})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Number != 4 {
		t.Fatalf("unexpected list result: %#v, %v", listed, err)
	}
	created, err := service.CreatePullRequest(PullRequestCreateRequest{
		Connection:   connection,
		Title:        "Ship it",
		Body:         "Ready",
		SourceBranch: "feature",
		TargetBranch: "main",
	})
	if err != nil || created.Number != 9 {
		t.Fatalf("unexpected create result: %#v, %v", created, err)
	}
	if !sawList || !sawCreate {
		t.Fatalf("sawList=%v sawCreate=%v", sawList, sawCreate)
	}
}

func TestPullRequestServiceGitHubDetailIncludesDiffAndMutations(t *testing.T) {
	repoPath := createPullRequestTestRepo(t, "https://github.com/acme/widgets.git")
	service := NewPullRequestService(nil, &pullRequestSecretStub{token: "token"})
	var paths []string
	service.setHTTPTransport(pullRequestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		if req.URL.Path == "/repos/acme/widgets/pulls/7" && req.Method == http.MethodGet {
			if strings.Contains(req.Header.Get("Accept"), "diff") {
				return pullRequestHTTPResponse(http.StatusOK, "diff --git a/a.go b/a.go\n", nil), nil
			}
			return pullRequestHTTPResponse(http.StatusOK, `{"number":7,"title":"Fix","body":"Details","state":"open","user":{"login":"octo"},"head":{"ref":"feature"},"base":{"ref":"main"}}`, nil), nil
		}
		if req.URL.Path == "/repos/acme/widgets/issues/7/comments" {
			return pullRequestHTTPResponse(http.StatusCreated, `{"id":11,"body":"Looks good","user":{"login":"octo"}}`, nil), nil
		}
		if req.URL.Path == "/repos/acme/widgets/pulls/7/reviews" {
			data, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(data), `"event":"REQUEST_CHANGES"`) {
				t.Fatalf("unexpected review body: %s", data)
			}
			return pullRequestHTTPResponse(http.StatusOK, `{"id":12,"body":"Please add a test","state":"CHANGES_REQUESTED","user":{"login":"octo"}}`, nil), nil
		}
		return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
	}))
	connection := PullRequestConnection{RepoPath: repoPath, ConfigID: "github-config"}
	detail, err := service.GetPullRequest(PullRequestGetRequest{Connection: connection, Number: 7})
	if err != nil || !strings.Contains(detail.Diff, "diff --git") {
		t.Fatalf("unexpected detail: %#v, %v", detail, err)
	}
	comment, err := service.AddComment(PullRequestCommentRequest{Connection: connection, Number: 7, Body: "Looks good"})
	if err != nil || comment.ID != 11 {
		t.Fatalf("unexpected comment: %#v, %v", comment, err)
	}
	review, err := service.SubmitReview(PullRequestReviewRequest{Connection: connection, Number: 7, Action: PullRequestReviewRequestChanges, Body: "Please add a test"})
	if err != nil || review.State != "changes_requested" {
		t.Fatalf("unexpected review: %#v, %v", review, err)
	}
	if len(paths) != 4 {
		t.Fatalf("unexpected requests: %#v", paths)
	}
}

func TestPullRequestServiceGitLabCapabilitiesRejectRequestChanges(t *testing.T) {
	repoPath := createPullRequestTestRepo(t, "git@gitlab.com:group/widgets.git")
	service := NewPullRequestService(nil, &pullRequestSecretStub{token: "token"})
	repo, err := service.DetectRepository(PullRequestConnection{RepoPath: repoPath})
	if err != nil {
		t.Fatalf("DetectRepository: %v", err)
	}
	if !repo.Capabilities.CanApprove || repo.Capabilities.CanRequestChanges {
		t.Fatalf("unexpected capabilities: %#v", repo.Capabilities)
	}
	_, err = service.SubmitReview(PullRequestReviewRequest{
		Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "gitlab-config"},
		Number:     3,
		Action:     PullRequestReviewRequestChanges,
		Body:       "Changes needed",
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected capability error, got %v", err)
	}
}

func TestPullRequestServiceSecurityBoundaries(t *testing.T) {
	t.Run("response body retains request context", func(t *testing.T) {
		repoPath := createPullRequestTestRepo(t, "git@github.com:acme/widgets.git")
		service := NewPullRequestService(nil, &pullRequestSecretStub{token: "token"})
		service.setHTTPTransport(pullRequestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: &pullRequestContextBody{
					ctx:  req.Context(),
					data: []byte("[]"),
				},
			}, nil
		}))
		_, err := service.ListPullRequests(PullRequestListRequest{Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "github"}})
		if err != nil {
			t.Fatalf("response body read after premature context cancellation: %v", err)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		repoPath := createPullRequestTestRepo(t, "git@github.com:acme/widgets.git")
		service := NewPullRequestService(nil, &pullRequestSecretStub{})
		_, err := service.ListPullRequests(PullRequestListRequest{Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "missing"}})
		if err == nil || !strings.Contains(err.Error(), "authentication is not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("private GitLab host", func(t *testing.T) {
		repoPath := createPullRequestTestRepo(t, "ssh://git@127.0.0.1/group/widgets.git")
		service := NewPullRequestService(nil, &pullRequestSecretStub{token: "token"})
		service.setHTTPTransport(pullRequestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("blocked private host reached transport")
			return nil, nil
		}))
		_, err := service.ListPullRequests(PullRequestListRequest{Connection: PullRequestConnection{
			RepoPath:      repoPath,
			ConfigID:      "gitlab",
			GitLabBaseURL: "https://127.0.0.1",
		}})
		if err == nil || !strings.Contains(err.Error(), "private") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("redirect rejected", func(t *testing.T) {
		repoPath := createPullRequestTestRepo(t, "git@github.com:acme/widgets.git")
		service := NewPullRequestService(nil, &pullRequestSecretStub{token: "token"})
		service.setHTTPTransport(pullRequestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return pullRequestHTTPResponse(http.StatusFound, "", map[string]string{"Location": "https://github.com/login"}), nil
		}))
		_, err := service.ListPullRequests(PullRequestListRequest{Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "github"}})
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("provider error is sanitized", func(t *testing.T) {
		repoPath := createPullRequestTestRepo(t, "git@github.com:acme/widgets.git")
		secret := "super-secret-token"
		service := NewPullRequestService(nil, &pullRequestSecretStub{token: secret})
		service.setHTTPTransport(pullRequestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return pullRequestHTTPResponse(http.StatusUnauthorized, `{"message":"bad token super-secret-token"}`, nil), nil
		}))
		_, err := service.ListPullRequests(PullRequestListRequest{Connection: PullRequestConnection{RepoPath: repoPath, ConfigID: "github"}})
		if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), secret) {
			t.Fatalf("error was not sanitized: %v", err)
		}
	})
}
