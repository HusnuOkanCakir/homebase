// The typed client for core's API.
//
// The dashboard is an ordinary API client with no special privileges. It
// authenticates the same way any client does, is bounded by the signed-in user's
// permissions, and never talks to Docker, systemd or the filesystem. When it
// needs something the API cannot express, the fix is to add the capability to
// the API — not to give the browser a side channel.

const BASE = "/api/v1";

/** The error envelope from schemas/error.schema.json. */
export interface ApiErrorBody {
  code: string;
  message: string;
  detail?: string;
  recoverable?: boolean;
  recovery?: string;
  request_id?: string;
}

/**
 * A failure the API described.
 *
 * `code` is the contract and is what code should branch on. `message` is
 * written for the person reading it and may be reworded at any time, so it is
 * shown rather than matched.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly detail: string | undefined;
  readonly recoverable: boolean;
  readonly recovery: string | undefined;
  readonly requestId: string | undefined;
  readonly status: number;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.detail = body.detail;
    this.recoverable = body.recoverable ?? false;
    this.recovery = body.recovery;
    this.requestId = body.request_id;
  }
}

/** A failure that never reached the server. */
export class NetworkError extends Error {
  constructor(cause: unknown) {
    super("Homebase is not responding.");
    this.name = "NetworkError";
    this.cause = cause;
  }
}

/**
 * How long to wait before giving up on a request.
 *
 * fetch has no default timeout, and a request that hangs rather than failing is
 * worse than one that fails: the interface waits forever with no way out. This
 * is not hypothetical — a machine part-way through a restart accepts the
 * connection and then never answers, which left the "restarting" screen
 * spinning indefinitely until this existed.
 */
const DEFAULT_TIMEOUT_MS = 15_000;

async function request<T>(
  path: string,
  init: RequestInit = {},
  timeoutMs = DEFAULT_TIMEOUT_MS,
): Promise<T> {
  let response: Response;
  try {
    response = await fetch(BASE + path, {
      ...init,
      headers: {
        "Content-Type": "application/json",
        ...(init.headers ?? {}),
      },
      // The session is a cookie; it has to travel.
      credentials: "same-origin",
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (cause) {
    // A server that has just been told to reboot stops answering, which is the
    // expected outcome rather than a fault. A timeout arrives here too, and is
    // the same thing as far as the caller is concerned: no answer.
    throw new NetworkError(cause);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  let payload: unknown;
  try {
    payload = text ? JSON.parse(text) : undefined;
  } catch {
    throw new ApiError(response.status, {
      code: "response.unreadable",
      message: "Homebase sent a reply that could not be understood.",
      detail: text.slice(0, 200),
    });
  }

  if (!response.ok) {
    const body = (payload as { error?: ApiErrorBody } | undefined)?.error;
    throw new ApiError(
      response.status,
      body ?? {
        code: "http." + response.status,
        message: "Something went wrong.",
      },
    );
  }

  return payload as T;
}

const get = <T>(path: string, timeoutMs?: number) =>
  request<T>(path, { method: "GET" }, timeoutMs);
const post = <T>(path: string, body?: unknown) =>
  request<T>(path, {
    method: "POST",
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });

// --- Types -------------------------------------------------------------------

export interface Health {
  status: "ok" | "degraded";
  version: string;
  hostd_reachable: boolean;
  uptime_seconds: number;
}

export interface User {
  id: string;
  username: string;
  permissions: string[];
  created_at: string;
}

export interface SystemInfo {
  hostname: string;
  os: string;
  kernel: string;
  architecture: string;
  virtualised: boolean;
  uptime_seconds: number;
  cpu: { model: string; cores: number; threads: number };
  memory: { total_bytes: number; available_bytes: number };
  load_average: [number, number, number];
  power: {
    /** null means the machine has no battery — which is not the same as "not on battery". */
    on_battery: boolean | null;
    battery_percent: number | null;
  };
}

export type JobState =
  | "queued"
  | "running"
  | "cancelling"
  | "cancelled"
  | "succeeded"
  | "failed"
  | "rolling_back"
  | "rolled_back"
  | "rollback_failed";

export interface Job {
  job_id: string;
  operation: string;
  state: JobState;
  stage: string | null;
  progress: number | null;
  message: string | null;
  cancellable: boolean;
  error: ApiErrorBody | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export const isTerminal = (state: JobState): boolean =>
  ["succeeded", "failed", "cancelled", "rolled_back", "rollback_failed"].includes(state);

// --- Calls -------------------------------------------------------------------

export const api = {
  /**
   * Is the server answering?
   *
   * The short timeout is deliberate: this is polled while waiting for a machine
   * to come back, and the answer that matters is "not yet" arriving promptly
   * rather than a request that eventually succeeds.
   */
  health: (timeoutMs = 4000) => get<Health>("/health", timeoutMs),

  setupStatus: () => get<{ needs_setup: boolean }>("/setup"),

  createAdministrator: (username: string, password: string) =>
    post<{ user: User; expires_at: string }>("/setup", { username, password }),

  login: (username: string, password: string) =>
    post<{ user: User; expires_at: string }>("/auth/login", { username, password }),

  logout: () => post<void>("/auth/logout"),

  me: () => get<User>("/auth/me"),

  system: () => get<SystemInfo>("/system"),

  /**
   * Restart the server.
   *
   * `confirm` must be the server's own name. hostd requires the target to be
   * named so that a confirmation cannot be replayed against a different
   * machine — the dashboard passes it through rather than inventing one.
   */
  reboot: (confirm: string, reason?: string) =>
    post<Job>("/system/reboot", reason === undefined ? { confirm } : { confirm, reason }),

  job: (id: string) => get<Job>(`/jobs/${encodeURIComponent(id)}`),

  jobs: () => get<{ items: Job[]; total: number }>("/jobs"),
};
