package services

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
)

const (
	pullRequestDefaultPages   = 3
	pullRequestMaximumPages   = 5
	pullRequestDefaultPerPage = 50
	pullRequestMaximumPerPage = 100
)

type PullRequestService struct {
	git     *GitService
	secrets PullRequestSecretResolver

	mu        sync.RWMutex
	transport http.RoundTripper
}

func NewPullRequestService(gitService *GitService, secrets PullRequestSecretResolver) *PullRequestService {
	return &PullRequestService{git: gitService, secrets: secrets}
}

// setHTTPTransport injects a transport for deterministic tests. Production
// calls use NewSSRFSafeTransport, which revalidates DNS results at dial time.
//
//wails:ignore
func (s *PullRequestService) setHTTPTransport(transport http.RoundTripper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport = transport
}

func (s *PullRequestService) DetectRepository(connection PullRequestConnection) (PullRequestRepository, error) {
	repoPath := strings.TrimSpace(connection.RepoPath)
	if repoPath == "" {
		return PullRequestRepository{}, fmt.Errorf("repository path is required")
	}
	if s.git != nil {
		if err := s.git.validatePath(repoPath); err != nil {
			return PullRequestRepository{}, fmt.Errorf("repository path is not allowed: %w", err)
		}
	}
	repository, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return PullRequestRepository{}, fmt.Errorf("open git repository: %w", err)
	}
	remoteName := strings.TrimSpace(connection.RemoteName)
	if remoteName == "" {
		remoteName = "origin"
	}
	remote, err := repository.Remote(remoteName)
	if err != nil && connection.RemoteName == "" {
		remotes, listErr := repository.Remotes()
		if listErr == nil && len(remotes) == 1 {
			remote = remotes[0]
			err = nil
		}
	}
	if err != nil {
		return PullRequestRepository{}, fmt.Errorf("git remote %q is not configured", remoteName)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return PullRequestRepository{}, fmt.Errorf("git remote %q has no URL", remoteName)
	}
	return parsePullRequestRemoteURL(urls[0], connection.GitLabBaseURL)
}

func (s *PullRequestService) ListPullRequests(input PullRequestListRequest) (PullRequestListResult, error) {
	repository, token, err := s.prepare(input.Connection)
	if err != nil {
		return PullRequestListResult{}, err
	}
	perPage := input.PerPage
	if perPage <= 0 {
		perPage = pullRequestDefaultPerPage
	}
	if perPage > pullRequestMaximumPerPage {
		perPage = pullRequestMaximumPerPage
	}
	maxPages := input.MaxPages
	if maxPages <= 0 {
		maxPages = pullRequestDefaultPages
	}
	if maxPages > pullRequestMaximumPages {
		maxPages = pullRequestMaximumPages
	}
	state, err := normalizePullRequestState(input.State, repository.Provider)
	if err != nil {
		return PullRequestListResult{}, err
	}
	result := PullRequestListResult{Repository: repository, Items: make([]PullRequest, 0)}
	for page := 1; page <= maxPages; page++ {
		query := url.Values{
			"state":    []string{state},
			"per_page": []string{strconv.Itoa(perPage)},
			"page":     []string{strconv.Itoa(page)},
		}
		var hasNext bool
		switch repository.Provider {
		case PullRequestProviderGitHub:
			var payload []githubPullRequest
			headers, requestErr := s.doJSON(repository, token, http.MethodGet, githubPullRequestPath(repository, 0), query, nil, &payload)
			if requestErr != nil {
				return PullRequestListResult{}, requestErr
			}
			for _, item := range payload {
				result.Items = append(result.Items, mapGitHubPullRequest(item))
			}
			hasNext = strings.Contains(headers.Get("Link"), `rel="next"`)
		case PullRequestProviderGitLab:
			var payload []gitlabMergeRequest
			headers, requestErr := s.doJSON(repository, token, http.MethodGet, gitlabMergeRequestPath(repository, 0), query, nil, &payload)
			if requestErr != nil {
				return PullRequestListResult{}, requestErr
			}
			for _, item := range payload {
				result.Items = append(result.Items, mapGitLabMergeRequest(item))
			}
			hasNext = strings.TrimSpace(headers.Get("X-Next-Page")) != ""
		}
		if !hasNext {
			return result, nil
		}
		if page == maxPages {
			result.Truncated = true
		}
	}
	return result, nil
}

func (s *PullRequestService) GetPullRequest(input PullRequestGetRequest) (PullRequest, error) {
	if input.Number <= 0 {
		return PullRequest{}, fmt.Errorf("pull request number must be positive")
	}
	repository, token, err := s.prepare(input.Connection)
	if err != nil {
		return PullRequest{}, err
	}
	switch repository.Provider {
	case PullRequestProviderGitHub:
		var payload githubPullRequest
		path := githubPullRequestPath(repository, input.Number)
		if _, err := s.doJSON(repository, token, http.MethodGet, path, nil, nil, &payload); err != nil {
			return PullRequest{}, err
		}
		diff, err := s.doRaw(repository, token, http.MethodGet, path, "application/vnd.github.v3.diff")
		if err != nil {
			return PullRequest{}, err
		}
		result := mapGitHubPullRequest(payload)
		result.Diff = diff
		return result, nil
	case PullRequestProviderGitLab:
		var payload gitlabMergeRequest
		path := gitlabMergeRequestPath(repository, input.Number)
		if _, err := s.doJSON(repository, token, http.MethodGet, path, nil, nil, &payload); err != nil {
			return PullRequest{}, err
		}
		var changes struct {
			Changes []struct {
				OldPath string `json:"old_path"`
				NewPath string `json:"new_path"`
				Diff    string `json:"diff"`
			} `json:"changes"`
		}
		if _, err := s.doJSON(repository, token, http.MethodGet, path+"/changes", nil, nil, &changes); err != nil {
			return PullRequest{}, err
		}
		var diff strings.Builder
		for _, change := range changes.Changes {
			fmt.Fprintf(&diff, "diff --git a/%s b/%s\n%s", change.OldPath, change.NewPath, change.Diff)
			if !strings.HasSuffix(change.Diff, "\n") {
				diff.WriteByte('\n')
			}
			if int64(diff.Len()) > pullRequestMaxDiffBody {
				return PullRequest{}, fmt.Errorf("provider response exceeds the diff size limit")
			}
		}
		result := mapGitLabMergeRequest(payload)
		result.Diff = diff.String()
		return result, nil
	default:
		return PullRequest{}, fmt.Errorf("unsupported pull request provider")
	}
}

func (s *PullRequestService) CreatePullRequest(input PullRequestCreateRequest) (PullRequest, error) {
	if err := validatePullRequestText("title", input.Title, 512, true); err != nil {
		return PullRequest{}, err
	}
	if err := validatePullRequestText("body", input.Body, 1<<20, false); err != nil {
		return PullRequest{}, err
	}
	if err := validatePullRequestBranch(input.SourceBranch); err != nil {
		return PullRequest{}, fmt.Errorf("source branch: %w", err)
	}
	if err := validatePullRequestBranch(input.TargetBranch); err != nil {
		return PullRequest{}, fmt.Errorf("target branch: %w", err)
	}
	repository, token, err := s.prepare(input.Connection)
	if err != nil {
		return PullRequest{}, err
	}
	switch repository.Provider {
	case PullRequestProviderGitHub:
		body := struct {
			Title string `json:"title"`
			Body  string `json:"body,omitempty"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Draft bool   `json:"draft,omitempty"`
		}{input.Title, input.Body, input.SourceBranch, input.TargetBranch, input.Draft}
		var payload githubPullRequest
		if _, err := s.doJSON(repository, token, http.MethodPost, githubPullRequestPath(repository, 0), nil, body, &payload); err != nil {
			return PullRequest{}, err
		}
		return mapGitHubPullRequest(payload), nil
	case PullRequestProviderGitLab:
		body := struct {
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			Title        string `json:"title"`
			Description  string `json:"description,omitempty"`
		}{input.SourceBranch, input.TargetBranch, input.Title, input.Body}
		var payload gitlabMergeRequest
		if _, err := s.doJSON(repository, token, http.MethodPost, gitlabMergeRequestPath(repository, 0), nil, body, &payload); err != nil {
			return PullRequest{}, err
		}
		return mapGitLabMergeRequest(payload), nil
	default:
		return PullRequest{}, fmt.Errorf("unsupported pull request provider")
	}
}

func (s *PullRequestService) AddComment(input PullRequestCommentRequest) (PullRequestComment, error) {
	if input.Number <= 0 {
		return PullRequestComment{}, fmt.Errorf("pull request number must be positive")
	}
	if err := validatePullRequestText("comment", input.Body, 1<<20, true); err != nil {
		return PullRequestComment{}, err
	}
	repository, token, err := s.prepare(input.Connection)
	if err != nil {
		return PullRequestComment{}, err
	}
	body := struct {
		Body string `json:"body"`
	}{input.Body}
	switch repository.Provider {
	case PullRequestProviderGitHub:
		var payload githubComment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), input.Number)
		if _, err := s.doJSON(repository, token, http.MethodPost, path, nil, body, &payload); err != nil {
			return PullRequestComment{}, err
		}
		return PullRequestComment{ID: payload.ID, Body: payload.Body, Author: mapGitHubUser(payload.User), WebURL: payload.HTMLURL, CreatedAt: payload.CreatedAt}, nil
	case PullRequestProviderGitLab:
		var payload gitlabNote
		path := gitlabMergeRequestPath(repository, input.Number) + "/notes"
		if _, err := s.doJSON(repository, token, http.MethodPost, path, nil, body, &payload); err != nil {
			return PullRequestComment{}, err
		}
		return PullRequestComment{ID: payload.ID, Body: payload.Body, Author: mapGitLabUser(payload.Author), WebURL: payload.WebURL, CreatedAt: payload.CreatedAt}, nil
	default:
		return PullRequestComment{}, fmt.Errorf("unsupported pull request provider")
	}
}

func (s *PullRequestService) SubmitReview(input PullRequestReviewRequest) (PullRequestReview, error) {
	if input.Number <= 0 {
		return PullRequestReview{}, fmt.Errorf("pull request number must be positive")
	}
	if err := validatePullRequestText("review", input.Body, 1<<20, false); err != nil {
		return PullRequestReview{}, err
	}
	repository, token, err := s.prepare(input.Connection)
	if err != nil {
		return PullRequestReview{}, err
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	switch repository.Provider {
	case PullRequestProviderGitHub:
		event := ""
		switch action {
		case PullRequestReviewApprove:
			event = "APPROVE"
		case PullRequestReviewRequestChanges:
			event = "REQUEST_CHANGES"
		case PullRequestReviewComment:
			event = "COMMENT"
		default:
			return PullRequestReview{}, fmt.Errorf("unsupported review action")
		}
		if action == PullRequestReviewRequestChanges && strings.TrimSpace(input.Body) == "" {
			return PullRequestReview{}, fmt.Errorf("request changes review requires a message")
		}
		body := struct {
			Body  string `json:"body,omitempty"`
			Event string `json:"event"`
		}{input.Body, event}
		var payload githubReview
		path := githubPullRequestPath(repository, input.Number) + "/reviews"
		if _, err := s.doJSON(repository, token, http.MethodPost, path, nil, body, &payload); err != nil {
			return PullRequestReview{}, err
		}
		return PullRequestReview{
			ID: payload.ID, Body: payload.Body, State: normalizeGitHubReviewState(payload.State),
			Author: mapGitHubUser(payload.User), WebURL: payload.HTMLURL, CreatedAt: payload.SubmittedAt,
		}, nil
	case PullRequestProviderGitLab:
		switch action {
		case PullRequestReviewRequestChanges:
			return PullRequestReview{}, fmt.Errorf("request changes review is not supported by GitLab")
		case PullRequestReviewComment:
			comment, err := s.addCommentPrepared(repository, token, input.Number, input.Body)
			if err != nil {
				return PullRequestReview{}, err
			}
			return PullRequestReview{ID: comment.ID, Body: comment.Body, State: "commented", Author: comment.Author, WebURL: comment.WebURL, CreatedAt: comment.CreatedAt}, nil
		case PullRequestReviewApprove:
			path := gitlabMergeRequestPath(repository, input.Number) + "/approve"
			if _, err := s.doJSON(repository, token, http.MethodPost, path, nil, struct{}{}, nil); err != nil {
				return PullRequestReview{}, err
			}
			return PullRequestReview{Body: input.Body, State: "approved"}, nil
		default:
			return PullRequestReview{}, fmt.Errorf("unsupported review action")
		}
	default:
		return PullRequestReview{}, fmt.Errorf("unsupported pull request provider")
	}
}

func (s *PullRequestService) addCommentPrepared(repository PullRequestRepository, token string, number int, bodyText string) (PullRequestComment, error) {
	if err := validatePullRequestText("comment", bodyText, 1<<20, true); err != nil {
		return PullRequestComment{}, err
	}
	body := struct {
		Body string `json:"body"`
	}{bodyText}
	var payload gitlabNote
	if _, err := s.doJSON(repository, token, http.MethodPost, gitlabMergeRequestPath(repository, number)+"/notes", nil, body, &payload); err != nil {
		return PullRequestComment{}, err
	}
	return PullRequestComment{ID: payload.ID, Body: payload.Body, Author: mapGitLabUser(payload.Author), WebURL: payload.WebURL, CreatedAt: payload.CreatedAt}, nil
}

func (s *PullRequestService) prepare(connection PullRequestConnection) (PullRequestRepository, string, error) {
	repository, err := s.DetectRepository(connection)
	if err != nil {
		return PullRequestRepository{}, "", err
	}
	if s.secrets == nil {
		return PullRequestRepository{}, "", fmt.Errorf("authentication service is unavailable")
	}
	configID := strings.TrimSpace(connection.ConfigID)
	if configID == "" {
		return PullRequestRepository{}, "", fmt.Errorf("authentication is not configured")
	}
	token, err := s.secrets.getAPIKeyForConfig(configID)
	if err != nil {
		return PullRequestRepository{}, "", fmt.Errorf("authentication configuration could not be read")
	}
	if strings.TrimSpace(token) == "" {
		return PullRequestRepository{}, "", fmt.Errorf("authentication is not configured")
	}
	return repository, token, nil
}
