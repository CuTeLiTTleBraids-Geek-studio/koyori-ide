package services

const (
	PullRequestProviderGitHub = "github"
	PullRequestProviderGitLab = "gitlab"

	PullRequestReviewApprove        = "approve"
	PullRequestReviewRequestChanges = "request_changes"
	PullRequestReviewComment        = "comment"
)

type PullRequestSecretResolver interface {
	getAPIKeyForConfig(configID string) (string, error)
}

type PullRequestConnection struct {
	RepoPath      string `json:"repoPath"`
	ConfigID      string `json:"configId"`
	RemoteName    string `json:"remoteName,omitempty"`
	GitLabBaseURL string `json:"gitlabBaseUrl,omitempty"`
}

type PullRequestCapabilities struct {
	CanCreate         bool `json:"canCreate"`
	CanComment        bool `json:"canComment"`
	CanApprove        bool `json:"canApprove"`
	CanRequestChanges bool `json:"canRequestChanges"`
}

type PullRequestRepository struct {
	Provider     string                  `json:"provider"`
	Host         string                  `json:"host"`
	Owner        string                  `json:"owner"`
	Name         string                  `json:"name"`
	ProjectPath  string                  `json:"projectPath"`
	WebURL       string                  `json:"webUrl"`
	APIBaseURL   string                  `json:"apiBaseUrl"`
	Capabilities PullRequestCapabilities `json:"capabilities"`
}

type PullRequestUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

type PullRequest struct {
	ID           int64           `json:"id"`
	Number       int             `json:"number"`
	Title        string          `json:"title"`
	Body         string          `json:"body,omitempty"`
	State        string          `json:"state"`
	Author       PullRequestUser `json:"author"`
	SourceBranch string          `json:"sourceBranch"`
	TargetBranch string          `json:"targetBranch"`
	Draft        bool            `json:"draft"`
	WebURL       string          `json:"webUrl,omitempty"`
	CreatedAt    string          `json:"createdAt,omitempty"`
	UpdatedAt    string          `json:"updatedAt,omitempty"`
	Mergeable    *bool           `json:"mergeable,omitempty"`
	Diff         string          `json:"diff,omitempty"`
}

type PullRequestListRequest struct {
	Connection PullRequestConnection `json:"connection"`
	State      string                `json:"state,omitempty"`
	PerPage    int                   `json:"perPage,omitempty"`
	MaxPages   int                   `json:"maxPages,omitempty"`
}

type PullRequestListResult struct {
	Repository PullRequestRepository `json:"repository"`
	Items      []PullRequest         `json:"items"`
	Truncated  bool                  `json:"truncated"`
}

type PullRequestGetRequest struct {
	Connection PullRequestConnection `json:"connection"`
	Number     int                   `json:"number"`
}

type PullRequestCreateRequest struct {
	Connection   PullRequestConnection `json:"connection"`
	Title        string                `json:"title"`
	Body         string                `json:"body,omitempty"`
	SourceBranch string                `json:"sourceBranch"`
	TargetBranch string                `json:"targetBranch"`
	Draft        bool                  `json:"draft,omitempty"`
}

type PullRequestCommentRequest struct {
	Connection PullRequestConnection `json:"connection"`
	Number     int                   `json:"number"`
	Body       string                `json:"body"`
}

type PullRequestComment struct {
	ID        int64           `json:"id"`
	Body      string          `json:"body"`
	Author    PullRequestUser `json:"author"`
	WebURL    string          `json:"webUrl,omitempty"`
	CreatedAt string          `json:"createdAt,omitempty"`
}

type PullRequestReviewRequest struct {
	Connection PullRequestConnection `json:"connection"`
	Number     int                   `json:"number"`
	Action     string                `json:"action"`
	Body       string                `json:"body,omitempty"`
}

type PullRequestReview struct {
	ID        int64           `json:"id"`
	Body      string          `json:"body,omitempty"`
	State     string          `json:"state"`
	Author    PullRequestUser `json:"author"`
	WebURL    string          `json:"webUrl,omitempty"`
	CreatedAt string          `json:"createdAt,omitempty"`
}
