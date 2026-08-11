// Koyori IDE 模块 · Pull Requests。
// 喵，这是 Koyori IDE 的 Pull Requests 模块（前端实现）~
import { computed, reactive } from "vue";
import { pullRequestService } from "@/api/pullRequests";
import { errorMessage } from "@/lib/errors";
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
  PullRequestReviewAction,
  PullRequestReviewRequest,
} from "@/types/pullRequests";

export type PullRequestAccessState =
  | "idle"
  | "ready"
  | "authentication-required"
  | "permission-denied"
  | "unsupported-provider"
  | "error";

export interface PullRequestBackend {
  detectRepository(connection: PullRequestConnection): Promise<PullRequestRepository>;
  listPullRequests(request: PullRequestListRequest): Promise<PullRequestListResult>;
  getPullRequest(request: PullRequestGetRequest): Promise<PullRequest>;
  createPullRequest(request: PullRequestCreateRequest): Promise<PullRequest>;
  addComment(request: PullRequestCommentRequest): Promise<PullRequestComment>;
  submitReview(request: PullRequestReviewRequest): Promise<PullRequestReview>;
}

interface PullRequestState {
  connection: PullRequestConnection;
  repository: PullRequestRepository | null;
  items: PullRequest[];
  selected: PullRequest | null;
  filter: string;
  stateFilter: "open" | "closed" | "all";
  loading: boolean;
  detailLoading: boolean;
  submitting: boolean;
  truncated: boolean;
  error: string | null;
  access: PullRequestAccessState;
  lastAction: string | null;
  activeView: "list" | "create";
  sourceControlView: "changes" | "pullRequests";
}

function emptyConnection(): PullRequestConnection {
  return { repoPath: "", configId: "" };
}

export const pullRequestState = reactive<PullRequestState>({
  connection: emptyConnection(),
  repository: null,
  items: [],
  selected: null,
  filter: "",
  stateFilter: "open",
  loading: false,
  detailLoading: false,
  submitting: false,
  truncated: false,
  error: null,
  access: "idle",
  lastAction: null,
  activeView: "list",
  sourceControlView: "changes",
});

export const filteredPullRequests = computed(() => {
  const query = pullRequestState.filter.trim().toLowerCase();
  return pullRequestState.items.filter((item) => {
    if (pullRequestState.stateFilter !== "all" && item.state !== pullRequestState.stateFilter) {
      return false;
    }
    if (!query) return true;
    return [
      String(item.number),
      item.title,
      item.author.login,
      item.sourceBranch,
      item.targetBranch,
    ].some((value) => value.toLowerCase().includes(query));
  });
});

let backend: PullRequestBackend | null = null;
let listGeneration = 0;
let detailGeneration = 0;

export function setPullRequestBackend(next: PullRequestBackend | null): void {
  backend = next;
}

function getBackend(): PullRequestBackend {
  return backend ?? pullRequestService;
}

export function configurePullRequests(connection: PullRequestConnection): void {
  const normalized: PullRequestConnection = {
    repoPath: connection.repoPath.trim(),
    configId: connection.configId.trim(),
    remoteName: connection.remoteName?.trim() || undefined,
    gitlabBaseUrl: connection.gitlabBaseUrl?.trim() || undefined,
  };
  const changed = JSON.stringify(normalized) !== JSON.stringify(pullRequestState.connection);
  pullRequestState.connection = normalized;
  if (!changed) return;
  listGeneration += 1;
  detailGeneration += 1;
  pullRequestState.repository = null;
  pullRequestState.items = [];
  pullRequestState.selected = null;
  pullRequestState.truncated = false;
  pullRequestState.error = null;
  pullRequestState.access = "idle";
  pullRequestState.lastAction = null;
}

export async function loadPullRequests(): Promise<boolean> {
  const generation = ++listGeneration;
  if (!pullRequestState.connection.repoPath) {
    setPullRequestError(new Error("Open a repository to view pull requests"));
    return false;
  }
  pullRequestState.loading = true;
  pullRequestState.error = null;
  pullRequestState.lastAction = null;
  try {
    const result = await getBackend().listPullRequests({
      connection: { ...pullRequestState.connection },
      state: pullRequestState.stateFilter,
      perPage: 50,
      maxPages: 5,
    });
    if (generation !== listGeneration) return false;
    pullRequestState.repository = result.repository;
    pullRequestState.items = result.items ?? [];
    pullRequestState.truncated = result.truncated;
    pullRequestState.access = "ready";
    return true;
  } catch (error: unknown) {
    if (generation !== listGeneration) return false;
    setPullRequestError(error);
    return false;
  } finally {
    if (generation === listGeneration) {
      pullRequestState.loading = false;
    }
  }
}

export async function loadPullRequest(number: number): Promise<boolean> {
  const generation = ++detailGeneration;
  pullRequestState.detailLoading = true;
  pullRequestState.error = null;
  try {
    const result = await getBackend().getPullRequest({
      connection: { ...pullRequestState.connection },
      number,
    });
    if (generation !== detailGeneration) return false;
    pullRequestState.selected = result;
    pullRequestState.access = "ready";
    return true;
  } catch (error: unknown) {
    if (generation !== detailGeneration) return false;
    setPullRequestError(error);
    return false;
  } finally {
    if (generation === detailGeneration) {
      pullRequestState.detailLoading = false;
    }
  }
}

export async function createPullRequest(
  input: Omit<PullRequestCreateRequest, "connection">,
): Promise<boolean> {
  pullRequestState.submitting = true;
  pullRequestState.error = null;
  pullRequestState.lastAction = null;
  try {
    const created = await getBackend().createPullRequest({
      ...input,
      connection: { ...pullRequestState.connection },
    });
    pullRequestState.items = [
      created,
      ...pullRequestState.items.filter((item) => item.number !== created.number),
    ];
    pullRequestState.selected = created;
    pullRequestState.activeView = "list";
    pullRequestState.access = "ready";
    pullRequestState.lastAction = "created";
    return true;
  } catch (error: unknown) {
    setPullRequestError(error);
    return false;
  } finally {
    pullRequestState.submitting = false;
  }
}

export async function addPullRequestComment(body: string): Promise<boolean> {
  const selected = pullRequestState.selected;
  if (!selected) {
    setPullRequestError(new Error("Select a pull request before commenting"));
    return false;
  }
  if (pullRequestState.repository && !pullRequestState.repository.capabilities.canComment) {
    setPermissionError("Comments are not supported for this provider");
    return false;
  }
  pullRequestState.submitting = true;
  pullRequestState.error = null;
  try {
    await getBackend().addComment({
      connection: { ...pullRequestState.connection },
      number: selected.number,
      body,
    });
    pullRequestState.lastAction = "commented";
    return true;
  } catch (error: unknown) {
    setPullRequestError(error);
    return false;
  } finally {
    pullRequestState.submitting = false;
  }
}

export async function submitPullRequestReview(
  action: PullRequestReviewAction,
  body = "",
): Promise<boolean> {
  const selected = pullRequestState.selected;
  if (!selected) {
    setPullRequestError(new Error("Select a pull request before reviewing"));
    return false;
  }
  const capabilities = pullRequestState.repository?.capabilities;
  if (
    (action === "approve" && capabilities && !capabilities.canApprove) ||
    (action === "request_changes" && capabilities && !capabilities.canRequestChanges)
  ) {
    setPermissionError(`Review action ${action} is not supported for this provider`);
    return false;
  }
  pullRequestState.submitting = true;
  pullRequestState.error = null;
  try {
    const review = await getBackend().submitReview({
      connection: { ...pullRequestState.connection },
      number: selected.number,
      action,
      body,
    });
    pullRequestState.lastAction = review.state || action;
    pullRequestState.access = "ready";
    return true;
  } catch (error: unknown) {
    setPullRequestError(error);
    return false;
  } finally {
    pullRequestState.submitting = false;
  }
}

export function closePullRequestDetail(): void {
  detailGeneration += 1;
  pullRequestState.selected = null;
  pullRequestState.detailLoading = false;
  pullRequestState.error = null;
  pullRequestState.lastAction = null;
}

export function setPullRequestView(view: "list" | "create"): void {
  pullRequestState.activeView = view;
  pullRequestState.error = null;
  pullRequestState.lastAction = null;
}

export function setSourceControlView(view: "changes" | "pullRequests"): void {
  pullRequestState.sourceControlView = view;
}

function setPermissionError(message: string): void {
  pullRequestState.error = message;
  pullRequestState.access = "permission-denied";
}

function setPullRequestError(error: unknown): void {
  const message = errorMessage(error);
  const normalized = message.toLowerCase();
  pullRequestState.error = message;
  if (normalized.includes("authentication") || normalized.includes("credential")) {
    pullRequestState.access = "authentication-required";
  } else if (normalized.includes("access denied") || normalized.includes("permission")) {
    pullRequestState.access = "permission-denied";
  } else if (normalized.includes("unsupported git provider") || normalized.includes("not supported")) {
    pullRequestState.access = "unsupported-provider";
  } else {
    pullRequestState.access = "error";
  }
}

export function resetPullRequestStore(): void {
  listGeneration += 1;
  detailGeneration += 1;
  pullRequestState.connection = emptyConnection();
  pullRequestState.repository = null;
  pullRequestState.items = [];
  pullRequestState.selected = null;
  pullRequestState.filter = "";
  pullRequestState.stateFilter = "open";
  pullRequestState.loading = false;
  pullRequestState.detailLoading = false;
  pullRequestState.submitting = false;
  pullRequestState.truncated = false;
  pullRequestState.error = null;
  pullRequestState.access = "idle";
  pullRequestState.lastAction = null;
  pullRequestState.activeView = "list";
  pullRequestState.sourceControlView = "changes";
}
