package services

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func parsePullRequestRemoteURL(remoteURL, gitLabBaseURL string) (PullRequestRepository, error) {
	host, projectPath, err := parseGitRemoteLocation(remoteURL)
	if err != nil {
		return PullRequestRepository{}, err
	}
	parts := strings.Split(projectPath, "/")
	if len(parts) < 2 {
		return PullRequestRepository{}, fmt.Errorf("git remote must include an owner and repository")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return PullRequestRepository{}, fmt.Errorf("git remote contains an invalid project path")
		}
	}
	name := parts[len(parts)-1]
	owner := strings.Join(parts[:len(parts)-1], "/")
	repository := PullRequestRepository{
		Host:        host,
		Owner:       owner,
		Name:        name,
		ProjectPath: projectPath,
		WebURL:      "https://" + host + "/" + projectPath,
	}
	switch host {
	case "github.com":
		repository.Provider = PullRequestProviderGitHub
		repository.APIBaseURL = "https://api.github.com"
		repository.Capabilities = PullRequestCapabilities{
			CanCreate: true, CanComment: true, CanApprove: true, CanRequestChanges: true,
		}
		return repository, nil
	case "gitlab.com":
		repository.Provider = PullRequestProviderGitLab
		repository.APIBaseURL = "https://gitlab.com"
		repository.Capabilities = PullRequestCapabilities{CanCreate: true, CanComment: true, CanApprove: true}
		return repository, nil
	}
	base, err := validateConfiguredGitLabBaseURL(gitLabBaseURL)
	if err != nil {
		return PullRequestRepository{}, err
	}
	if !strings.EqualFold(base.Hostname(), host) {
		return PullRequestRepository{}, fmt.Errorf("configured GitLab host does not match git remote host")
	}
	repository.Provider = PullRequestProviderGitLab
	repository.APIBaseURL = strings.TrimRight(base.String(), "/")
	repository.Capabilities = PullRequestCapabilities{CanCreate: true, CanComment: true, CanApprove: true}
	return repository, nil
}

func parseGitRemoteLocation(remoteURL string) (string, string, error) {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return "", "", fmt.Errorf("git remote URL is empty")
	}
	var host, projectPath string
	if !strings.Contains(raw, "://") {
		at := strings.LastIndex(raw, "@")
		colon := strings.Index(raw, ":")
		if at >= 0 && colon > at {
			host = raw[at+1 : colon]
			projectPath = raw[colon+1:]
		}
	}
	if host == "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
			return "", "", fmt.Errorf("unsupported git remote URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https", "ssh", "git":
		default:
			return "", "", fmt.Errorf("unsupported git remote URL scheme")
		}
		if parsed.User != nil && parsed.Scheme == "https" {
			return "", "", fmt.Errorf("git remote URL must not contain HTTPS credentials")
		}
		host = parsed.Hostname()
		projectPath = parsed.Path
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	projectPath = strings.Trim(strings.TrimSpace(projectPath), "/")
	projectPath = strings.TrimSuffix(projectPath, ".git")
	if host == "" || projectPath == "" {
		return "", "", fmt.Errorf("git remote URL is missing a host or project path")
	}
	return host, projectPath, nil
}

func validateConfiguredGitLabBaseURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("unsupported git provider; configure the GitLab HTTPS host for self-managed GitLab")
	}
	base, err := url.Parse(raw)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil {
		return nil, fmt.Errorf("GitLab base URL must be an HTTPS origin without credentials")
	}
	if base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("GitLab base URL must not contain a path, query, or fragment")
	}
	base.Path = ""
	return base, nil
}

func normalizePullRequestState(state, provider string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if normalized == "" {
		normalized = "open"
	}
	switch normalized {
	case "open":
		if provider == PullRequestProviderGitLab {
			return "opened", nil
		}
		return "open", nil
	case "closed":
		return "closed", nil
	case "all":
		return "all", nil
	default:
		return "", fmt.Errorf("invalid pull request state")
	}
}

func validatePullRequestText(name, value string, maximum int, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds size limit", name)
	}
	return nil
}

func validatePullRequestBranch(branch string) error {
	trimmed := strings.TrimSpace(branch)
	if trimmed == "" {
		return fmt.Errorf("branch is required")
	}
	if len(trimmed) > 255 || strings.ContainsAny(trimmed, "\r\n\x00") {
		return fmt.Errorf("branch is invalid")
	}
	return nil
}

func githubPullRequestPath(repository PullRequestRepository, number int) string {
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(repository.Owner), url.PathEscape(repository.Name))
	if number > 0 {
		path += "/" + strconv.Itoa(number)
	}
	return path
}

func gitlabMergeRequestPath(repository PullRequestRepository, number int) string {
	path := "/api/v4/projects/" + url.PathEscape(repository.ProjectPath) + "/merge_requests"
	if number > 0 {
		path += "/" + strconv.Itoa(number)
	}
	return path
}

type githubUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type githubPullRequest struct {
	ID        int64      `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	User      githubUser `json:"user"`
	Draft     bool       `json:"draft"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	MergedAt  *string    `json:"merged_at"`
	Mergeable *bool      `json:"mergeable"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type githubComment struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	User      githubUser `json:"user"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt string     `json:"created_at"`
}

type githubReview struct {
	ID          int64      `json:"id"`
	Body        string     `json:"body"`
	State       string     `json:"state"`
	User        githubUser `json:"user"`
	HTMLURL     string     `json:"html_url"`
	SubmittedAt string     `json:"submitted_at"`
}

func mapGitHubUser(user githubUser) PullRequestUser {
	return PullRequestUser{Login: user.Login, AvatarURL: user.AvatarURL}
}

func mapGitHubPullRequest(input githubPullRequest) PullRequest {
	state := strings.ToLower(input.State)
	if input.MergedAt != nil {
		state = "merged"
	}
	return PullRequest{
		ID: input.ID, Number: input.Number, Title: input.Title, Body: input.Body, State: state,
		Author: mapGitHubUser(input.User), SourceBranch: input.Head.Ref, TargetBranch: input.Base.Ref,
		Draft: input.Draft, WebURL: input.HTMLURL, CreatedAt: input.CreatedAt, UpdatedAt: input.UpdatedAt,
		Mergeable: input.Mergeable,
	}
}

func normalizeGitHubReviewState(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes_requested"
	case "COMMENTED":
		return "commented"
	default:
		return strings.ToLower(state)
	}
}

type gitlabUser struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type gitlabMergeRequest struct {
	ID             int64      `json:"id"`
	IID            int        `json:"iid"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	State          string     `json:"state"`
	Author         gitlabUser `json:"author"`
	SourceBranch   string     `json:"source_branch"`
	TargetBranch   string     `json:"target_branch"`
	Draft          bool       `json:"draft"`
	WorkInProgress bool       `json:"work_in_progress"`
	WebURL         string     `json:"web_url"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
	MergedAt       *string    `json:"merged_at"`
	MergeStatus    string     `json:"detailed_merge_status"`
}

type gitlabNote struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	Author    gitlabUser `json:"author"`
	WebURL    string     `json:"web_url"`
	CreatedAt string     `json:"created_at"`
}

func mapGitLabUser(user gitlabUser) PullRequestUser {
	return PullRequestUser{Login: user.Username, AvatarURL: user.AvatarURL}
}

func mapGitLabMergeRequest(input gitlabMergeRequest) PullRequest {
	state := strings.ToLower(input.State)
	if input.MergedAt != nil || state == "merged" {
		state = "merged"
	} else if state == "opened" {
		state = "open"
	}
	var mergeable *bool
	if input.MergeStatus != "" {
		value := input.MergeStatus == "mergeable" || input.MergeStatus == "can_be_merged"
		mergeable = &value
	}
	return PullRequest{
		ID: input.ID, Number: input.IID, Title: input.Title, Body: input.Description, State: state,
		Author: mapGitLabUser(input.Author), SourceBranch: input.SourceBranch, TargetBranch: input.TargetBranch,
		Draft: input.Draft || input.WorkInProgress, WebURL: input.WebURL, CreatedAt: input.CreatedAt,
		UpdatedAt: input.UpdatedAt, Mergeable: mergeable,
	}
}
