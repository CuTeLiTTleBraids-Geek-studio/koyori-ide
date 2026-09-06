export interface WorkerReplacementOptions {
  previousRuntimeId: string;
  expectedVersion: string;
  readRuntimeId: () => Promise<string>;
  readVersion: () => Promise<string>;
  timeoutMs?: number;
  pollIntervalMs?: number;
  now?: () => number;
  sleep?: (milliseconds: number) => Promise<void>;
}

export interface WorkerReplacementResult {
  runtimeId: string;
  latestError: string | null;
}

export class WorkerReplacementTimeoutError extends Error {
  constructor(
    message: string,
    readonly latestError: string | null,
  ) {
    super(message);
    this.name = "WorkerReplacementTimeoutError";
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

export async function waitForWorkerReplacement({
  previousRuntimeId,
  expectedVersion,
  readRuntimeId,
  readVersion,
  timeoutMs = 30_000,
  pollIntervalMs = 50,
  now = Date.now,
  sleep = delay,
}: WorkerReplacementOptions): Promise<WorkerReplacementResult> {
  const deadline = now() + timeoutMs;
  let latestError: string | null = null;

  while (now() < deadline) {
    try {
      const runtimeId = await readRuntimeId();
      if (runtimeId === previousRuntimeId) {
        latestError = "Worker runtime identity did not change";
      } else {
        const version = await readVersion();
        if (version === expectedVersion) {
          return { runtimeId, latestError };
        }
        latestError = `reactivated version was ${version}`;
      }
    } catch (error: unknown) {
      latestError = errorMessage(error);
    }
    await sleep(pollIntervalMs);
  }

  throw new WorkerReplacementTimeoutError(
    `Worker recovery timed out: ${latestError ?? "Worker did not reactivate"}`,
    latestError,
  );
}
