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
  remaining?: number;
  detail?: string;
  since: number; // unix seconds
}

export interface Snapshot {
  peers: Peer[];
  extensions: ExtSync[];
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

export const EMPTY_SNAPSHOT: Snapshot = { peers: [], extensions: [] };
