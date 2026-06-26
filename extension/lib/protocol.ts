// Protocol types for the daemon <-> extension localhost WebSocket. These mirror
// the Go side in internal/statusws and internal/status — keep them in sync.
//
// After the auth frame the channel is bidirectional:
//   server -> client  { type: "status", snapshot }      live status
//   server -> client  { type: "beacon", beacon }         publish this to storage.sync
//   client -> server  { type: "peers", beacons }         peers seen in storage.sync
// Sealed beacons are base64 strings (Go marshals []byte as base64).

export type PeerState =
  | "discovering"
  | "signaling"
  | "hole-punching"
  | "connected"
  | "unreachable";

export type Link = "lan" | "direct-nat";

export type Reason = "peer-offline" | "nat-traversal-failed" | "auth-failed";

export type SyncState =
  | "in-sync"
  | "pushing"
  | "pulling"
  | "stale"
  | "blocked"
  | "error"
  | "pending";

export interface Peer {
  device_id: string;
  name?: string;
  state: PeerState;
  link?: Link;
  reason?: Reason;
  detail?: string;
  since: number; // unix seconds
}

export interface ExtSync {
  extension: string;
  device_id: string;
  state: SyncState;
  remaining?: number; // chunks left, for pushing/pulling
  total?: number; // total chunks this transfer, for a determinate progress bar
  detail?: string;
  since: number; // unix seconds
}

export type LogLevel = "info" | "ok" | "warn" | "error";

export interface LogEntry {
  time: number; // unix seconds
  level: LogLevel;
  message: string;
}

// Self is the local device — so the UI can label "this device" / "you".
export interface Self {
  device_id: string;
  name?: string;
}

export interface Snapshot {
  self: Self;
  peers: Peer[];
  extensions: ExtSync[];
  log: LogEntry[];
}

// Server -> client frames.
export interface StatusMsg {
  type: "status";
  snapshot: Snapshot;
}
export interface BeaconMsg {
  type: "beacon";
  beacon: string; // base64 sealed beacon to publish to storage.sync
}
export type ServerMsg = StatusMsg | BeaconMsg;

// Client -> server frames.
export interface AuthMsg {
  token: string;
}
export interface PeersMsg {
  type: "peers";
  beacons: string[]; // base64 sealed peer beacons read from storage.sync
}

export const EMPTY_SNAPSHOT: Snapshot = {
  self: { device_id: "" },
  peers: [],
  extensions: [],
  log: [],
};

// Runtime enumerations of the string unions above. The element types tie each
// array to its union, so a typo or a value the union doesn't permit fails to
// compile. protocol.contract.test.ts asserts these equal the Go source-of-truth
// sets in internal/status (via the generated enums.json fixture), so an enum
// added on one side and forgotten on the other fails a test. Keep them complete.
export const PEER_STATES: readonly PeerState[] = [
  "discovering",
  "signaling",
  "hole-punching",
  "connected",
  "unreachable",
];
export const LINKS: readonly Link[] = ["lan", "direct-nat"];
export const REASONS: readonly Reason[] = [
  "peer-offline",
  "nat-traversal-failed",
  "auth-failed",
];
export const SYNC_STATES: readonly SyncState[] = [
  "in-sync",
  "pushing",
  "pulling",
  "stale",
  "blocked",
  "error",
  "pending",
];
export const LOG_LEVELS: readonly LogLevel[] = ["info", "ok", "warn", "error"];
export const SERVER_MSG_TYPES: readonly ServerMsg["type"][] = [
  "status",
  "beacon",
];
