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

/**
 * `unknown` means Homebase could not ask the container runtime.
 *
 * Kept separate from `not_installed` because the two look the same in an
 * interface and are entirely different facts — one of them means a working
 * application Homebase cannot see, and offering to install it again on top of a
 * running one is how somebody loses data.
 */
export type ApplicationState =
  | "not_installed"
  | "stopped"
  | "running"
  | "failed"
  | "unknown";

export interface Application {
  id: string;
  name: string;
  summary?: string;
  state: ApplicationState;
  /** Null where the state is unknown; `false` would be a claim nobody can make. */
  installed: boolean | null;
  /**
   * The application's own health result, or null.
   *
   * **null is not "unhealthy".** It means the application declares no check, or
   * has not been checked yet. Showing "unhealthy" for null would report every
   * starting application as broken.
   */
  health: string | null;
  image: string;
  version?: string;
  internal_port?: number;
  started_at: string | null;
  exit_code: number | null;
  /** Where its data lives, so a user can be told what uninstalling leaves behind. */
  data_path: string;
}

export interface ApplicationList {
  items: Application[];
  total: number;
  /**
   * When false, every application reads `not_installed` because its real state
   * is unknown. The list is still populated: the machine knows what it *can*
   * install even when it cannot see what is running.
   */
  docker_available: boolean;
  /** Manifests that failed to load, mapped to why. */
  unavailable?: Record<string, string>;
}

export type EventSeverity = "info" | "warning" | "error" | "critical";

export interface Event {
  event_id: string;
  type: string;
  severity: EventSeverity;
  subject: string | null;
  reason: string | null;
  recoverable: boolean | null;
  message: string | null;
  occurred_at: string;
}

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

  // --- Applications ---------------------------------------------------------
  //
  // Note what is absent: there is no argument anywhere here for an image, a
  // port, a mount or an environment variable. The dashboard cannot describe a
  // container because the API cannot, and the API cannot because hostd builds
  // it from a manifest the browser never sees. See ADR-0012.

  apps: () => get<ApplicationList>("/apps"),

  app: (id: string) => get<Application>(`/apps/${encodeURIComponent(id)}`),

  /**
   * The application's own log output.
   *
   * A longer timeout than usual: an application that has just failed is the one
   * whose logs somebody most wants, and it is also the one most likely to be
   * slow to answer.
   */
  appLogs: (id: string, lines = 200) =>
    get<{ id: string; lines: number; logs: string }>(
      `/apps/${encodeURIComponent(id)}/logs?lines=${lines}`,
      30_000,
    ),

  installApp: (id: string) => post<Job>(`/apps/${encodeURIComponent(id)}/install`),

  startApp: (id: string) => post<Job>(`/apps/${encodeURIComponent(id)}/start`),

  /**
   * The destructive operations take the application's id as confirmation.
   *
   * Passed through rather than invented here: the API requires the request to
   * name what it is acting on, so a confirmation cannot be replayed against a
   * different application, and typing the name is what makes somebody notice
   * which one they are about to stop.
   */
  stopApp: (id: string, confirm: string) =>
    post<Job>(`/apps/${encodeURIComponent(id)}/stop`, { confirm }),

  restartApp: (id: string, confirm: string) =>
    post<Job>(`/apps/${encodeURIComponent(id)}/restart`, { confirm }),

  uninstallApp: (id: string, confirm: string) =>
    post<Job>(`/apps/${encodeURIComponent(id)}/uninstall`, { confirm }),

  removeAppData: (id: string, confirm: string) =>
    post<Job>(`/apps/${encodeURIComponent(id)}/data/remove`, { confirm }),

  // --- Events ---------------------------------------------------------------

  events: (limit = 20) => get<{ items: Event[]; total: number }>(`/events?limit=${limit}`),
};

/**
 * Watch a job until it finishes.
 *
 * Polled rather than streamed. The event stream announces that something
 * happened, but a job's progress is a question about one specific job, and
 * asking for it directly means the interface cannot show a stale percentage
 * because it missed a message.
 *
 * Returns a function that stops watching. `onUpdate` is called with every
 * observation including the last, so a caller does not have to fetch again to
 * learn how it ended.
 */
export function watchJob(
  id: string,
  onUpdate: (job: Job) => void,
  onError: (error: unknown) => void,
  intervalMs = 1000,
): () => void {
  let stopped = false;

  const poll = async () => {
    while (!stopped) {
      try {
        const job = await api.job(id);
        if (stopped) return;
        onUpdate(job);
        if (isTerminal(job.state)) return;
      } catch (caught) {
        if (stopped) return;
        // A single missed poll is not a failure: installing an application can
        // load the machine enough that one request times out, and giving up
        // there would report a working install as broken.
        if (!(caught instanceof NetworkError)) {
          onError(caught);
          return;
        }
      }
      await new Promise((resolve) => setTimeout(resolve, intervalMs));
    }
  };

  void poll();
  return () => {
    stopped = true;
  };
}
