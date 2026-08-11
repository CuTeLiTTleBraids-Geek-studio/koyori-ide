// Koyori IDE 模块 · Pull Requests。
// 喵，这是 Koyori IDE 的 Pull Requests 模块（前端实现）~
import * as PullRequestServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/pullrequestservice.js";
import type {
  PullRequest,
  PullRequestComment,
  PullRequestCommentRequest,
  PullRequestConnection,
  PullRequestCreateRequest,
  PullRequestGetRequest,
  PullRequestListRequest,
  PullRequestListResult,
  PullRequestRepository,
  PullRequestReview,
  PullRequestReviewRequest,
} from "@/types/pullRequests";

export const pullRequestService = {
  detectRepository: (connection: PullRequestConnection) =>
    PullRequestServiceBindings.DetectRepository(connection) as Promise<PullRequestRepository>,
  listPullRequests: (request: PullRequestListRequest) =>
    PullRequestServiceBindings.ListPullRequests(request) as Promise<PullRequestListResult>,
  getPullRequest: (request: PullRequestGetRequest) =>
    PullRequestServiceBindings.GetPullRequest(request) as Promise<PullRequest>,
  createPullRequest: (request: PullRequestCreateRequest) =>
    PullRequestServiceBindings.CreatePullRequest(request) as Promise<PullRequest>,
  addComment: (request: PullRequestCommentRequest) =>
    PullRequestServiceBindings.AddComment(request) as Promise<PullRequestComment>,
  submitReview: (request: PullRequestReviewRequest) =>
    PullRequestServiceBindings.SubmitReview(request) as Promise<PullRequestReview>,
};
