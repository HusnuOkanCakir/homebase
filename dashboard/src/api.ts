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

/** A person on this server, as an administrator sees them. */
export interface Account {
  id: string;
  username: string;
  role: Role;
  /**
   * Whether they have ever signed in. An invitation nobody has accepted and an
   * account in daily use look identical without it.
   */
  has_signed_in: boolean;
  created_at: string;
  last_login_at?: string;
}

export type Role = "administrator" | "member" | "limited";

/** What comes back once, when an account is created or a code reissued. */
export interface JoiningCode {
  id?: string;
  username: string;
  role?: Role;
  joining_code: string;
  message: string;
}


/** What the local assistant is, and whether it can be used at all. */
export interface AssistantStatus {
  available: boolean;
  /** The model being served, named for reading rather than for loading. */
  model?: string;
  /** Why it is unavailable, in a sentence meant for a person. */
  reason?: string;
  max_turns: number;
  max_chars: number;
  /**
   * Every model this account may select.
   *
   * Filtered by permission on the server, not here: a model somebody may not
   * use is not one they should learn exists from a greyed-out entry.
   */
  models: AssistantModel[];
}

export interface AssistantModel {
  id: string;
  label: string;
  /** What the server underneath reports, e.g. Qwen3.8-4B-Q4_K_M. */
  name?: string;
  available: boolean;
  reason?: string;
  /**
   * A model whose refusal behaviour a third party removed. Never the default,
   * and said on screen for as long as it is selected.
   */
  unrestricted: boolean;
}

export interface AssistantMessage {
  role: "user" | "assistant";
  content: string;
}


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
  /**
   * Whether a magic packet will switch this machine on.
   *
   * Three states, not two. `wake_on_lan_known: false` means the card would not
   * say — which is different from a card that said no, and a screen that
   * offered "wake it over the network" on the strength of a guess would be
   * telling somebody their server can be switched back on when it cannot.
   */
  wake_on_lan?: boolean;
  wake_on_lan_supported?: boolean;
  wake_on_lan_known?: boolean;
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

/**
 * A folder published onto the local network over SMB.
 *
 * `available` and being shared are separate: a share whose disk has been
 * unplugged is still configured and still listed, it simply has nothing behind
 * it. Hiding it would be reporting the configuration wrongly to fix a disk.
 */
export interface SharedFolder {
  name: string;
  location: string;
  read_only: boolean;
  added_at: string;
  path: string;
  available: boolean;
  /** What to type on Windows, composed by the server: \\name\share. */
  address: string;
  /** Who may open it, by Homebase username. Absent or empty means everybody
   *  with an account, which is what every folder is until somebody says
   *  otherwise. There is deliberately no way to say "nobody". */
  access?: string[];
}

export interface ShareStatus {
  /** Whether the file server is on this machine at all. It is not part of the
   *  base installation — a listening service that is off until asked for is one
   *  that cannot be misconfigured. */
  installed: boolean;
  /** Whether it is actually serving. Separate from being installed and from
   *  there being shares: "configured but not running" is a real state, and the
   *  one worth saying loudly, because from the other end it looks identical to
   *  a share that was never made. */
  running: boolean;
  shares: SharedFolder[];
  /** The accounts that may connect. Names only — Homebase cannot read a
   *  password back and would not report one if it could. */
  users: string[];
  server_name: string;
}

/**
 * What the server says about itself.
 *
 * Only `hostname` and `version` are always there. Everything else needs
 * `system.read`, and an account without it — somebody given a login to fetch a
 * file — gets the two fields and nothing more.
 *
 * Optional in the type because they are optional in fact. Declaring them
 * mandatory is how `system.temperature.message` came to be read on an object
 * that had no temperature, which took the whole dashboard down for exactly the
 * accounts that could not have caused it.
 */
export interface SystemInfo {
  hostname: string;
  version?: string;
  os?: string;
  kernel?: string;
  architecture?: string;
  virtualised?: boolean;
  uptime_seconds?: number;
  cpu?: { model: string; cores: number; threads: number };
  /**
   * The graphics hardware, and the name for it that will still be right after
   * a reboot.
   *
   * `render_node` is what every other tool prints. `stable_path` is what to
   * actually configure an application with — the numbering in the first one is
   * assigned in probe order, and on this project's own test machine installing
   * a driver made two cards swap numbers, silently pointing the media server at
   * a different chip than the one it had been using.
   */
  graphics?: {
    name: string;
    driver: string;
    render_node: string;
    stable_path?: string;
    accelerates_video: boolean;
  }[];
  memory: { total_bytes: number; available_bytes: number };
  load_average: [number, number, number];
  power: {
    /** null means the machine has no battery — which is not the same as "not on battery". */
    on_battery: boolean | null;
    battery_percent: number | null;
  };
  /**
   * How hot the machine is.
   *
   * `celsius: null` means it cannot tell — every VM, and some real hardware.
   * Never zero, which would look wonderfully cool.
   */
  temperature: {
    celsius: number | null;
    sensor?: string;
    state?: "ok" | "warm" | "hot";
    /** Only set when something is worth telling somebody. */
    message?: string;
  };
  /**
   * What the cooling is doing, and who is deciding.
   *
   * Only meaningful beside the temperature: loud and cool is a fan fault, loud
   * and hot is a heatsink full of dust, and from across a room they are the
   * same sound. `rpm: null` is a machine with no sensor — never zero, which
   * would read as a seized fan.
   */
  fan: {
    rpm: number | null;
    percent: number | null;
    label?: string;
    controlled?: "firmware" | "manual" | "full";
    message?: string;
  };
}

/** One reading from the record. */
/**
 * One reading.
 *
 * Everything measurable is nullable, and null is not zero. A machine with no
 * thermal sensor, a reading taken before there was a previous one to subtract
 * from, a counter that reset across a reboot — all of those are "not measured",
 * and a zero would draw as a cold, idle, silent server that was doing nothing.
 */
export interface ThermalSample {
  time: string;
  celsius: number | null;
  fan_rpm: number | null;
  fan_percent: number | null;
  /** Busy time between this reading and the one before it. */
  cpu_percent: number | null;
  memory_percent: number | null;
  download_bytes_per_second: number | null;
  upload_bytes_per_second: number | null;
  state?: string;
  load?: number;
}

export interface ThermalHistory {
  samples: ThermalSample[];
  hottest_celsius: number | null;
  coolest_celsius: number | null;
  average_celsius: number | null;
  loudest_rpm: number | null;
  quietest_rpm: number | null;
  since?: string;
  /**
   * Whether readings are being taken at all.
   *
   * An empty history on a newly installed machine and an empty history because
   * the recorder is broken look identical, and only one of them needs anybody
   * to do something.
   */
  recording: boolean;
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
  /** One character for the tile — an emoji, usually. Absent is normal. */
  icon?: string;
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
  /**
   * Where to open it, and whether anything other than this machine can.
   *
   * Both, because the address alone cannot say which. An application on
   * loopback has a real address that is not there from the computer showing
   * this page, and a link to it would fail in a way that looks like the server
   * being broken.
   */
  url?: string;
  reachable_from_network: boolean;
  /**
   * What the person who installed it still has to do.
   *
   * For several applications this is the difference between working and
   * usable: one that is running, reachable, and asking for a password nobody
   * was given is indistinguishable from one that is broken.
   */
  after_install?: string;
  started_at: string | null;
  exit_code: number | null;
  /** Where its data lives, so a user can be told what uninstalling leaves behind. */
  data_path: string;
  /**
   * A privilege this application holds that most do not — absent for the ones
   * that need none.
   *
   * Shown before it is installed, not after. The point of declaring a
   * relaxation per application is that somebody can decline it, and a
   * disclosure that arrives once the container is running is a notification
   * rather than a choice.
   */
  elevation?: AppElevation;
}

export interface AppElevation {
  /** "starts_as_root" or "runs_as_root" — one gives root up, the other does not. */
  kind: string;
  /** One sentence, composed by the server rather than by the manifest's author. */
  summary: string;
  /** The manifest's own words, for somebody who wants them. */
  reason: string;
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

/**
 * Wireless.
 *
 * `available` is false on much of the hardware Homebase runs on, and is the
 * first thing a screen needs to know — "no networks in range" and "this machine
 * has no wireless" send somebody to entirely different places.
 *
 * `has_wired_connection` decides how frightening the screen has to be: with a
 * cable in, a failed attempt costs nothing.
 *
 * There is no passphrase field, in either direction. The server never returns
 * one.
 */
export interface WifiStatus {
  available: boolean;
  interface?: string;
  connected: boolean;
  ssid?: string;
  addresses?: string[];
  signal?: number;
  bars?: number;
  configured: boolean;
  has_wired_connection: boolean;
}

/**
 * Remote access, over Wireguard.
 *
 * `configured` and `running` are separate facts, and so is `ever_connected`: a
 * tunnel that is set up, running, and has never seen a handshake is almost
 * always a router that has not been told to forward the port — the one part of
 * this Homebase cannot do and the only part that usually goes wrong.
 */
export interface VPNStatus {
  configured: boolean;
  running: boolean;
  hostname?: string;
  port: number;
  devices: VPNDevice[];
  ever_connected: boolean;
  dns: {
    configured: boolean;
    provider?: string;
    name?: string;
    last_result?: string;
    last_checked?: string;
    message?: string;
  };
  /**
   * Tailscale, which Homebase does not manage and does report.
   *
   * On a connection behind carrier-grade NAT the Wireguard half of remote
   * access can never work, and this is what is actually carrying the traffic.
   * A screen that named only the part Homebase controls would be honest about
   * itself and useless to the person reading it.
   */
  tailscale: {
    installed: boolean;
    running: boolean;
    /** The daemon's own word: "Running", "NeedsLogin", "Stopped". */
    state?: string;
    /** What to type from away, e.g. homebase.tail9c4e2.ts.net */
    name?: string;
    addresses?: string[];
  };
  message?: string;
}

export interface VPNDevice {
  name: string;
  address: string;
  public_key: string;
  last_handshake?: string;
  transfer_rx?: number;
  transfer_tx?: number;
}

/**
 * A device's configuration, returned exactly once by the call that created it.
 *
 * It contains the device's private key and is stored nowhere. Losing it means
 * removing the device and adding it again — which is why the server says so in
 * `message` rather than leaving the interface to imply it.
 */
export interface NewVPNDevice extends VPNDevice {
  config: string;
  /** The same configuration as a PNG data URI, for scanning with a phone. */
  qr_image?: string;
  message: string;
}

export interface WifiNetwork {
  ssid: string;
  signal: number;
  bars: number;
  security: "open" | "wep" | "wpa" | "wpa3";
  current: boolean;
}

/**
 * A diagnostic file, and what is in it.
 *
 * `excludes` is shown to the user rather than kept in the documentation,
 * because the question "is this safe to send to somebody?" is asked at the
 * moment of sending.
 */
export interface Diagnostics {
  path: string;
  bytes?: number;
  created_at: string;
  includes: string[];
  excludes: string[];
  message: string;
}

export interface RepairStep {
  what: string;
  done?: string;
  problem?: string;
}

export interface RepairResult {
  steps: RepairStep[];
  /** Zero means whatever is wrong is not something repair knows how to fix. */
  changed: number;
  healthy: boolean;
  message: string;
}

export interface FactoryResetResult {
  removed?: string[];
  kept?: string[];
  message: string;
}

/**
 * When backups happen without anybody pressing anything.
 *
 * `enabled` comes from systemd rather than from what was last asked for, and
 * `last_result` travels with it: a schedule is a promise kept nightly, and the
 * way it fails is silently. Anything that shows the promise has to show whether
 * it is being kept.
 */
export interface BackupSchedule {
  every: "daily" | "weekly" | "off";
  location?: string;
  description: string;
  enabled: boolean;
  next_run?: string;
  last_result?: "ok" | "failed";
}

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

  /** Everybody on this server. Requires `accounts.manage`. */
  accounts: () => get<{ accounts: Account[] }>("/accounts"),

  /**
   * Add somebody, and get the code they sign in with — once.
   *
   * The code is not recoverable. It is stored the way a password is, so nothing
   * can produce it again; `reissueJoiningCode` issues a replacement.
   */
  createAccount: (username: string, role: Role) =>
    post<JoiningCode>("/accounts", { username, role }),

  setAccountRole: (id: string, role: Role) =>
    post<{ id: string; username: string; role: Role }>(`/accounts/${id}/role`, { role }),

  /** The confirmation is their username, exactly. Their files are kept. */
  removeAccount: (id: string, confirm: string) =>
    post<{ removed: string; message: string }>(`/accounts/${id}/remove`, { confirm }),

  reissueJoiningCode: (id: string) =>
    post<JoiningCode>(`/accounts/${id}/joining-code`, undefined),

  /**
   * Whether this machine has a local model, and what it is.
   *
   * Answered even when there is none — `available: false` with a reason — so
   * the dashboard can say what is missing instead of hiding a tab silently.
   */
  assistant: () => get<AssistantStatus>("/assistant"),

  /**
   * How hot the machine has been, and how hard its fan has worked.
   *
   * `points` is capped because a month at five-minute intervals is nine
   * thousand readings — more than a chart this size can draw and more than a
   * browser should be asked to parse. The server thins rather than truncates,
   * so the shape survives.
   */
  systemHistory: (days = 7, points = 200) =>
    get<ThermalHistory>(`/system/history?days=${days}&points=${points}`),

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

  /**
   * Switch the server off.
   *
   * The same shape as a reboot and the same confirmation, because to the API
   * they are the same operation. The difference is entirely in what the caller
   * must have said first: a restart explains itself by ending, and this does
   * not — so whatever calls this is the last thing that will be able to tell
   * somebody how to switch the machine on again.
   */
  shutdown: (confirm: string, reason?: string) =>
    post<Job>("/system/shutdown", reason === undefined ? { confirm } : { confirm, reason }),

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

  // --- File sharing ---------------------------------------------------------

  shares: () => get<ShareStatus>("/shares"),

  /** Long: the first share installs the file server, which is a download. */
  addShare: (name: string, location: string, readOnly: boolean) =>
    post<ShareStatus>("/shares", { name, location, read_only: readOnly }, 600_000),

  /** Stops publishing it. The files stay exactly where they are. */
  removeShare: (name: string) =>
    post<ShareStatus>("/shares/remove", { name }, 120_000),

  setSharePassword: (username: string, password: string) =>
    post<ShareStatus>("/shares/users", { username, password }, 120_000),

  removeShareUser: (username: string) =>
    post<ShareStatus>("/shares/users/remove", { username }, 60_000),

  /** Who may open a folder. An empty list means everybody with an account. */
  setShareAccess: (name: string, access: string[]) =>
    post<{ name: string; access: string[]; everyone: boolean; message: string }>(
      "/shares/access",
      { name, access },
      120_000,
    ),

  // --- Remote access ------------------------------------------------------------

  vpn: () => get<VPNStatus>("/network/vpn"),

  /** Switching it on writes a configuration, starts the tunnel and opens a port
   *  in the firewall — so it is allowed longer than an ordinary request. */
  setUpVPN: (hostname: string) =>
    post<VPNStatus>("/network/vpn", { hostname }, 120_000),

  /** Closes the port first, then stops the tunnel. The keys are kept. */
  disableVPN: () => post<VPNStatus>("/network/vpn/disable", undefined, 60_000),

  addVPNDevice: (name: string) =>
    post<NewVPNDevice>("/network/vpn/devices", { name }, 60_000),

  removeVPNDevice: (name: string) =>
    post<VPNStatus>("/network/vpn/devices/remove", { name }, 60_000),

  setVPNDNS: (provider: string, name: string, token: string) =>
    post<VPNStatus>("/network/vpn/dns", { provider, name, token }, 120_000),

  clearVPNDNS: () => post<VPNStatus>("/network/vpn/dns/clear", undefined, 60_000),

  // --- Wireless ---------------------------------------------------------------

  wifiStatus: () => get<WifiStatus>("/network/wifi"),

  /** A POST because it takes seconds and is asked for by pressing a button. */
  scanWifi: () =>
    post<{ networks: WifiNetwork[]; message?: string }>(
      "/network/wifi/scan",
      undefined,
      120_000,
    ),

  /**
   * Join a network.
   *
   * Allowed longer than anything else in the API: if it fails, the server puts
   * the previous configuration back and applies it again *before* answering, so
   * this waits for the rollback too — which is the right thing to wait for.
   */
  joinWifi: (ssid: string, passphrase: string) =>
    post<WifiStatus>("/network/wifi", { ssid, passphrase }, 300_000),

  forgetWifi: () => post<WifiStatus>("/network/wifi/forget", undefined, 150_000),

  // --- When something is wrong ------------------------------------------------

  /**
   * Collect a diagnostic file. Long, because it reads a day of the journal —
   * and on a machine that is having trouble, that is when the journal is
   * longest.
   */
  collectDiagnostics: () =>
    post<Diagnostics>("/system/diagnostics", undefined, 180_000),

  /** Where the browser downloads the file from. Always the newest one. */
  diagnosticsDownloadURL: () => `${BASE}/system/diagnostics/download`,

  /**
   * Try to fix what is wrong. Long, because finishing an interrupted package
   * transaction is dpkg running maintainer scripts.
   */
  repair: () => post<RepairResult>("/system/repair", undefined, 720_000),

  /**
   * Remove every account and every setting. `confirm` must be the server's own
   * name — the one string specific to this machine.
   */
  factoryReset: (confirm: string, keepData: boolean) =>
    post<FactoryResetResult>(
      "/system/factory-reset",
      { confirm, keep_data: keepData },
      360_000,
    ),

  backupSchedule: () => get<BackupSchedule>("/backups/schedule"),

  /**
   * Choose how often backups run. Answers with the schedule as it now stands,
   * read back from systemd — so a request that was accepted and did not take
   * effect shows up as one.
   *
   * The destination is checked before this returns, which is why it is allowed
   * longer than an ordinary write.
   */
  setBackupSchedule: (every: BackupSchedule["every"], location?: string) =>
    post<BackupSchedule>("/backups/schedule", { every, location }, 60_000),

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

/**
 * Ask the local model something, and receive the answer as it is written.
 *
 * Streaming rather than a single response, because this hardware writes about
 * six or seven tokens a second: a paragraph is thirty seconds and a long answer
 * is over a minute. Waiting that long at a blank screen is indistinguishable
 * from a server that has hung, and people reload — which on a machine with one
 * inference slot makes the wait longer, not shorter.
 *
 * It does not go through `request`, which buffers the whole body and imposes a
 * fifteen-second timeout. Both are right for every other call and wrong for this
 * one.
 *
 * Returns a function that stops the answer. Aborting the request is what frees
 * the server's single slot, so "stop" has to actually cancel rather than just
 * hide the output.
 */
export function askAssistant(
  messages: AssistantMessage[],
  handlers: {
    onToken: (text: string) => void;
    /** `reason` is "length" when the answer hit the token ceiling. */
    onDone: (reason: string) => void;
    onError: (error: unknown) => void;
  },
  options: { think?: boolean; model?: string } = {},
): () => void {
  const controller = new AbortController();

  const run = async () => {
    let response: Response;
    try {
      response = await fetch(BASE + "/assistant/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          messages,
          think: options.think ?? false,
          // Omitted rather than sent empty, so a server that predates model
          // selection sees exactly the request it used to.
          ...(options.model ? { model: options.model } : {}),
        }),
        credentials: "same-origin",
        signal: controller.signal,
      });
    } catch (cause) {
      if (!controller.signal.aborted) handlers.onError(new NetworkError(cause));
      return;
    }

    if (!response.ok) {
      // The refusals — busy, too long, no model — are ordinary JSON, because
      // they happen before any streaming starts.
      let body: ApiErrorBody | undefined;
      try {
        body = ((await response.json()) as { error?: ApiErrorBody }).error;
      } catch {
        /* fall through to the generic message */
      }
      handlers.onError(
        new ApiError(
          response.status,
          body ?? { code: "http." + response.status, message: "Something went wrong." },
        ),
      );
      return;
    }

    if (!response.body) {
      handlers.onError(new NetworkError("this browser cannot read a streamed answer"));
      return;
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        // SSE frames are separated by a blank line. A frame can arrive split
        // across reads, so only whole ones are taken and the remainder is kept.
        let boundary = buffer.indexOf("\n\n");
        while (boundary !== -1) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          boundary = buffer.indexOf("\n\n");

          let event = "message";
          let data = "";
          for (const line of frame.split("\n")) {
            if (line.startsWith("event: ")) event = line.slice(7);
            else if (line.startsWith("data: ")) data += line.slice(6);
            // Lines beginning ":" are comments — the heartbeat and the
            // "connected" marker — and are meant to be ignored.
          }
          if (!data) continue;

          if (event === "token") {
            const parsed = JSON.parse(data) as { text?: string };
            if (parsed.text) handlers.onToken(parsed.text);
          } else if (event === "done") {
            handlers.onDone((JSON.parse(data) as { reason?: string }).reason ?? "stop");
            return;
          } else if (event === "failed") {
            handlers.onError(
              new ApiError(500, {
                code: "assistant.interrupted",
                message: "The answer stopped part-way through.",
                recoverable: true,
                recovery: "Ask again.",
              }),
            );
            return;
          }
        }
      }
      // The stream ended without saying so. Treat it as finished rather than as
      // an error: the text already on screen is real and worth keeping.
      handlers.onDone("stop");
    } catch (cause) {
      if (!controller.signal.aborted) handlers.onError(new NetworkError(cause));
    }
  };

  void run();
  return () => controller.abort();
}
