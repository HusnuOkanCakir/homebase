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
const post = <T>(path: string, body?: unknown, timeoutMs?: number) =>
  request<T>(
    path,
    {
      method: "POST",
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    },
    timeoutMs,
  );

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

/**
 * What the server will say about a recovery code, which is never the code
 * itself. It is stored the way a password is and cannot be shown twice.
 */
export interface RecoveryStatus {
  exists: boolean;
  issued_at?: string;
  last_used_at?: string;
}


/** One of the four packages a Homebase installation is made of. */
export interface Component {
  package: string;
  version: string;
  state: string;
}

/**
 * What this server is running.
 *
 * Four packages rather than one version string, because the interesting failure
 * is that they disagree — they move together by dependency, so apt cannot
 * produce a mixed set on purpose. Only an interrupted update can, and a machine
 * in that state usually still works, which is why nothing else notices.
 */
export interface UpdateStatus {
  version: string;
  consistent: boolean;
  interrupted: boolean;
  components: Component[];
  channel: string;
  origin: string;
}

export interface UpdateCheck {
  current: string;
  available: string;
  update_available: boolean;
  channel: string;
  reachable: boolean;
  detail?: string;
}

export interface UpdateChannel {
  channel: string;
  origin: string;
  reachable: boolean;
  detail?: string;
}

/**
 * How far an update got.
 *
 * `result` is empty while it is still running. That emptiness is the signal:
 * the server cannot remember anything across an update, because the update
 * restarts it.
 */
export interface UpdateProgress {
  stage: string;
  result?: "ok" | "failed";
  from?: string;
  to?: string;
  detail?: string;
  running: boolean;
}

/** One way the server is attached to a network. */
export interface NetworkInterface {
  name: string;
  kind: string;
  up: boolean;
  addresses?: string[];
  mac?: string;
}

/**
 * How the server is connected.
 *
 * `reachable` and `online` are separate deliberately: a server with an address
 * on a network whose broadband is down is a different problem from a server
 * with no address, and both look identical from a browser that will not load.
 */
export interface NetworkStatus {
  hostname: string;
  mdns_name: string;
  mdns_works: boolean;
  interfaces: NetworkInterface[];
  gateway?: string;
  nameservers?: string[];
  online: boolean;
  reachable: boolean;
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

// --- Storage -----------------------------------------------------------------

export interface Volume {
  device: string;
  path: string;
  /**
   * The filesystem's identity. Absent means this volume **cannot be assigned to
   * an application** — there is no way to find it again reliably once it has
   * been unplugged.
   */
  uuid?: string;
  label?: string;
  /** Empty means it was read and nothing recognisable was found. */
  filesystem?: string;
  /**
   * Homebase could not read this volume.
   *
   * **Not the same as an empty `filesystem`.** One means "read it, found
   * nothing"; this means "could not look". Never offer to erase on this basis:
   * it may be somebody's photographs behind a disk that is failing.
   */
  unreadable: boolean;
  size_bytes: number;
  mount_point?: string;
  read_only: boolean;
}

export interface Disk {
  /**
   * The kernel's current name. **Never store this.** It is assigned in
   * discovery order, so a disk unplugged as `sda` can come back as `sdb`.
   */
  device: string;
  path: string;
  model?: string;
  vendor?: string;
  size_bytes: number;
  removable: boolean;
  transport?: "usb" | "sata" | "nvme" | "virtio" | "sd-card" | "";
  /** Holds the running system. Homebase will not erase it, whatever is asked. */
  system: boolean;
  volumes: Volume[];
}

export interface StorageLocation {
  id: string;
  name: string;
  uuid: string;
  filesystem?: string;
  label?: string;
  added_at: string;
  mount_point: string;
  /** The disk is plugged in. */
  connected: boolean;
  /**
   * And usable. Separate from `connected`, because a disk that is present but
   * failed to mount is a different problem with a different remedy.
   */
  mounted: boolean;
  read_only: boolean;
  total_bytes?: number;
  available_bytes?: number;
  device?: string;
}

export interface ApplicationStorageSlot {
  id: string;
  type: "private" | "user-selected";
  description?: string;
  mount_path: string;
  read_only: boolean;
  location?: string;
  location_name?: string;
  ready: boolean;
  path?: string;
}

export interface ApplicationStorage {
  id: string;
  name: string;
  storage: ApplicationStorageSlot[];
  /** False means the application cannot start, and will refuse rather than run. */
  ready: boolean;
}

// --- Backup -------------------------------------------------------------------

export interface BackupSummary {
  id: string;
  location: string;
  created_at?: string;
  hostname?: string;
  version?: string;
  kind?: "configuration" | "full";
  files?: number;
  total_bytes?: number;
  applications?: string[];
  notes?: string[];
  /**
   * False for a backup with no readable manifest — it was interrupted.
   *
   * Shown rather than hidden: a folder that looks like a backup and is not is
   * exactly what nobody must rely on.
   */
  complete: boolean;
  problem?: string;
}

export interface RestorePreview {
  id: string;
  location: string;
  created_at?: string;
  hostname?: string;
  kind?: string;

  applications?: string[];
  unavailable_applications?: string[];
  applications_to_install?: string[];

  files_to_write: number;
  bytes_to_write?: number;
  /** How many files on this server would be replaced. The number that matters. */
  would_overwrite: number;

  /** Whether the backup's checksums still match, checked before it is offered. */
  verified: boolean;
  integrity_issues?: string[];

  notes?: string[];
  message: string;
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

  /**
   * Claim the server. The response carries the recovery code, which is the only
   * time it is ever sent — it is stored hashed and cannot be produced again.
   */
  createAdministrator: (username: string, password: string) =>
    post<{ user: User; expires_at: string; recovery_code?: string }>("/setup", {
      username,
      password,
    }),

  login: (username: string, password: string) =>
    post<{ user: User; expires_at: string }>("/auth/login", { username, password }),

  /**
   * Set a new password using the code from first-run setup, and sign in.
   *
   * Returns a replacement code, because somebody who recovers and is left
   * without one is a single forgotten password from where they started.
   */
  recover: (username: string, recoveryCode: string, newPassword: string) =>
    post<{ user: User; expires_at: string; recovery_code: string }>("/auth/recover", {
      username,
      recovery_code: recoveryCode,
      new_password: newPassword,
    }),

  /** Whether a recovery code exists, and when it was issued. Never the code. */
  recoveryStatus: () => get<RecoveryStatus>("/auth/recovery-code"),

  /** A fresh code, for somebody who has lost the paper. Invalidates the old one. */
  reissueRecoveryCode: () => post<{ recovery_code: string }>("/auth/recovery-code"),

  logout: () => post<void>("/auth/logout"),

  me: () => get<User>("/auth/me"),

  system: () => get<SystemInfo>("/system"),

  /**
   * How the server is connected. Slower than most reads, because deciding
   * whether the internet is reachable means waiting for something not to answer.
   */
  network: () => get<NetworkStatus>("/network", 25_000),

  /** What version this server is running. */
  updateStatus: () => get<UpdateStatus>("/system/update"),

  /**
   * Ask the channel whether there is anything newer.
   *
   * Slow by nature: it reaches the network, and deciding a repository is
   * unreachable means waiting for something not to answer.
   */
  checkForUpdate: () => post<UpdateCheck>("/system/update/check", undefined, 180_000),

  setUpdateChannel: (channel: string) =>
    post<UpdateChannel>("/system/update/channel", { channel }, 180_000),

  /**
   * Start an update. This returns as soon as the server has accepted it, and
   * not when it has finished — the update restarts the server that is answering,
   * so there is no response that could describe the outcome. Poll
   * `updateProgress` afterwards.
   */
  applyUpdate: () => post<{ started: boolean }>("/system/update/apply", undefined, 60_000),

  updateProgress: () => get<UpdateProgress>("/system/update/progress"),

  /**
   * Restart the server.
   *
   * `confirm` must be the server's own name. hostd requires the target to be
   * named so that a confirmation cannot be replayed against a different
   * machine — the dashboard passes it through rather than inventing one.
   */
  /**
   * Change what the server calls itself.
   *
   * Not a job, unlike everything else that changes the machine: renaming is
   * three file writes and a syscall, and the first thing this does afterwards
   * is read the name back.
   */
  rename: (name: string) =>
    post<{ previous: string; name: string; message: string }>("/system/name", { name }),

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

  // --- Storage ---------------------------------------------------------------
  //
  // Note what is absent: nothing here takes a device path or a mount point. A
  // disk is named by its filesystem UUID and a location by its id, because
  // `/dev/sdb` is not a stable name for anything — see ADR-0013.

  disks: () => get<{ items: Disk[]; total: number }>("/storage/disks", 30_000),

  locations: () =>
    get<{ items: StorageLocation[]; total: number }>("/storage/locations", 30_000),

  addLocation: (uuid: string, id: string, name: string) =>
    post<Job>("/storage/locations", { uuid, id, name }),

  removeLocation: (id: string, confirm: string) =>
    post<Job>(`/storage/locations/${encodeURIComponent(id)}/remove`, { confirm }),

  mountLocation: (id: string) =>
    post<Job>(`/storage/locations/${encodeURIComponent(id)}/mount`),

  unmountLocation: (id: string, confirm: string) =>
    post<Job>(`/storage/locations/${encodeURIComponent(id)}/unmount`, { confirm }),

  /**
   * Erase a disk. There is no undo and no backup is taken first.
   *
   * `confirm` must repeat the disk's own identifier — the UUID, or the device
   * path where it has no filesystem. Not a word like "yes": a confirmation
   * somebody can satisfy by reflex is not a confirmation, and this is the one
   * operation that can destroy data Homebase never created.
   */
  formatDisk: (target: { uuid?: string; device?: string }, label: string, confirm: string) =>
    post<Job>("/storage/format", { ...target, label, confirm }),

  appStorage: (id: string) =>
    get<ApplicationStorage>(`/apps/${encodeURIComponent(id)}/storage`),

  assignStorage: (app: string, storageID: string, location: string) =>
    post<Job>(`/apps/${encodeURIComponent(app)}/storage`, {
      storage_id: storageID,
      location,
    }),

  // --- Backup ----------------------------------------------------------------

  backups: (location: string) =>
    get<{ items: BackupSummary[]; total: number }>(
      `/backups?location=${encodeURIComponent(location)}`,
      60_000,
    ),

  createBackup: (location: string, includeData: boolean) =>
    post<Job>("/backups", { location, include_data: includeData }),

  /**
   * What restoring would do. Changes nothing.
   *
   * Asked before restoring is offered, never after — a preview somebody can skip
   * is a preview nobody sees.
   */
  previewRestore: (location: string, id: string) =>
    get<RestorePreview>(
      `/backups/${encodeURIComponent(id)}/preview?location=${encodeURIComponent(location)}`,
      300_000,
    ),

  verifyBackup: (location: string, id: string) =>
    post<Job>(
      `/backups/${encodeURIComponent(id)}/verify?location=${encodeURIComponent(location)}`,
    ),

  /** `confirm` must be the backup's own id — this overwrites what is here. */
  restoreBackup: (location: string, id: string, confirm: string) =>
    post<Job>(
      `/backups/${encodeURIComponent(id)}/restore?location=${encodeURIComponent(location)}`,
      { confirm },
    ),

  deleteBackup: (location: string, id: string, confirm: string) =>
    post<Job>(
      `/backups/${encodeURIComponent(id)}/delete?location=${encodeURIComponent(location)}`,
      { confirm },
    ),

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
