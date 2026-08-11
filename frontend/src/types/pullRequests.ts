// Koyori IDE 模块 · Pull Requests。
// 喵，这是 Koyori IDE 的 Pull Requests 模块（前端实现）~
export type PullRequestProvider = "github" | "gitlab";
export type PullRequestReviewAction = "approve" | "request_changes" | "comment";

export interface PullRequestConnection {
  repoPath: string;
  configId: string;
  remoteName?: string;
  gitlabBaseUrl?: string;
}

export interface PullRequestCapabilities {
  canCreate: boolean;
  canComment: boolean;
  canApprove: boolean;
  canRequestChanges: boolean;
}

export interface PullRequestRepository {
  provider: PullRequestProvider;
  host: string;
  owner: string;
  name: string;
  projectPath: string;
  webUrl: string;
  apiBaseUrl: string;
  capabilities: PullRequestCapabilities;
}

export interface PullRequestUser {
  login: string;
  avatarUrl?: string;
}

export interface PullRequest {
  id: number;
  number: number;
  title: string;
  body?: string;
  state: "open" | "closed" | "merged" | string;
  author: PullRequestUser;
  sourceBranch: string;
  targetBranch: string;
  draft: boolean;
  webUrl?: string;
  createdAt?: string;
  updatedAt?: string;
  mergeable?: boolean;
  diff?: string;
}

export interface PullRequestListRequest {
  connection: PullRequestConnection;
  state?: "open" | "closed" | "all";
  perPage?: number;
  maxPages?: number;
}

export interface PullRequestListResult {
  repository: PullRequestRepository;
  items: PullRequest[];
  truncated: boolean;
}

export interface PullRequestGetRequest {
  connection: PullRequestConnection;
  number: number;
}

export interface PullRequestCreateRequest {
  connection: PullRequestConnection;
  title: string;
  body?: string;
  sourceBranch: string;
  targetBranch: string;
  draft?: boolean;
}

export interface PullRequestCommentRequest {
  connection: PullRequestConnection;
  number: number;
  body: string;
}

export interface PullRequestComment {
  id: number;
  body: string;
  author: PullRequestUser;
  webUrl?: string;
  createdAt?: string;
}

export interface PullRequestReviewRequest {
  connection: PullRequestConnection;
  number: number;
  action: PullRequestReviewAction;
  body?: string;
}

export interface PullRequestReview {
  id: number;
  body?: string;
  state: string;
  author: PullRequestUser;
  webUrl?: string;
  createdAt?: string;
}

