package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/converge"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/discovery"
	"github.com/justinstimatze/gusset/internal/ffctl"
	"github.com/justinstimatze/gusset/internal/icewire"
	"github.com/justinstimatze/gusset/internal/policy"
	"github.com/justinstimatze/gusset/internal/profile"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
	"github.com/justinstimatze/gusset/internal/statusws"
	"github.com/justinstimatze/gusset/internal/store"
	"github.com/justinstimatze/gusset/internal/transport"
)

// browseGrace bounds one mDNS browse pass; the sync loop repeats it until the
// chosen lifetime elapses.
const browseGrace = 3 * time.Second

// peerStaleWindow is how long a device may go without re-advertising (no mDNS
// browse hit and no fresh beacon) before it is dropped from the peer list. It is
// far larger than one discovery pass, so a live peer that misses a single browse
// is never flickered out; it only sheds devices that have genuinely stopped
// publishing. A returning device reappears on its next advertisement.
const peerStaleWindow = 90 * time.Second

// syncCmd is the on-demand sync (docs/transport-and-security.md §8): it serves
// this device's allowlisted extensions and pulls a peer's newer ones over
// passphrase-authed mutual TLS, discovered on the LAN by mDNS. Its listener
// lifetime is the user's choice — one-shot window (default), bounded (--for), or
// indefinite (--watch) — so nobody is forced into an always-on process.
func syncCmd(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	forDur := fs.Duration("for", 30*time.Second, "stay reachable for this long, then exit")
	watch := fs.Bool("watch", false, "stay reachable indefinitely (until Ctrl-C)")
	once := fs.Bool("once", false, "exit as soon as the local pull finishes, skipping the reachable-back window (ideal for a one-way --force seed)")
	peerAddr := fs.String("peer", "", "dial this host:port directly, skipping mDNS discovery")
	rzDir := fs.String("rendezvous-dir", "", "exchange sealed beacons through this shared folder to reach peers off the LAN (Tier 1)")
	deviceID := fs.String("device-id", "", "override this device's stable unique id (default: persisted, generated from hostname)")
	deviceName := fs.String("device-name", "", "friendly name shown in the UI (default: hostname; persisted by `gusset init`)")
	stunServer := fs.String("stun", "", "STUN server host:port; enables the public-IP beacon candidate and the ICE hole-punch fallback (e.g. stun.l.google.com:19302)")
	listenAddr := fs.String("listen", "0.0.0.0:0", "address to listen on (host:port; :0 picks a free port)")
	extCSV := fs.String("extensions", "", "comma-separated extension IDs to sync (the allowlist)")
	overrideCSV := fs.String("override", "", "comma-separated sensitive extension IDs to force-enable")
	force := fs.Bool("force", false, "take the peer's copy unconditionally, ignoring last-writer-wins (use when seeding a new machine to match an established one)")
	restartFF := fs.Bool("restart-firefox", false, "close Firefox to apply, then relaunch it (destructive: closes your browser)")
	ffBin := fs.String("firefox-bin", "", "Firefox binary to relaunch with --restart-firefox (default: platform Firefox)")
	profileDir := fs.String("profile", "", "Firefox profile dir to sync (default: the active profile; or GUSSET_PROFILE)")
	wsAddr := fs.String("ws", "", "serve live status to the companion extension over a localhost WebSocket at this loopback host:port (e.g. 127.0.0.1:8765)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pass, err := readPassphrase(cfg)
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassphrase(pass); err != nil {
		return err
	}
	k, err := crypto.DeriveKeys(pass, cfg.SaltOrApp())
	if err != nil {
		return err
	}
	pol, err := chunk.DerivePolynomial(k)
	if err != nil {
		return err
	}

	profDir, installed, err := localProfile(profileOverride(*profileDir))
	if err != nil {
		return err
	}

	// A stale lock (crash residue, or a recycled PID) would otherwise block
	// apply even though no Firefox is running. Clearing it is provably safe —
	// ffctl only removes a lock no live Firefox holds — so do it unconditionally.
	if cleared, cerr := ffctl.ClearStale(profDir); cerr != nil {
		fmt.Fprintf(os.Stderr, "lock check: %v\n", cerr)
	} else if cleared {
		fmt.Println("cleared a stale Firefox lock (no running Firefox held the profile).")
	}

	// --force is a one-shot seed: under --watch it would re-clobber local with the
	// peer's copy on every pass (and ping-pong if both sides force), so warn.
	if *force && *watch {
		fmt.Fprintln(os.Stderr, "note: --force with --watch re-takes the peer's copy every pass; --force is meant as a one-shot seed (use --for instead).")
	}
	if *once && *watch {
		fmt.Fprintln(os.Stderr, "note: --once is ignored with --watch (--watch stays reachable until Ctrl-C).")
	}

	// Opt-in: close Firefox up front so incoming settings apply cleanly, and
	// relaunch it when the run ends. Only acts if Firefox is actually running.
	if *restartFF {
		if *watch {
			fmt.Fprintln(os.Stderr, "note: --restart-firefox with --watch keeps Firefox closed for the whole session.")
		}
		stopped, serr := ffctl.Stop(profDir, 30*time.Second)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "restart-firefox: %v\n", serr)
		} else if stopped {
			fmt.Println("Closed Firefox to apply incoming settings; will relaunch when done.")
			defer relaunchFirefox(*ffBin)
		}
	}

	pl := buildPolicy(cfg, *extCSV, *overrideCSV)
	allow := func(extID string) bool { return pl.Evaluate(extID).Allowed }

	// What we can offer: installed AND allowlisted.
	var offerIDs []string
	for id := range installed {
		if allow(id) {
			offerIDs = append(offerIDs, id)
		}
	}
	if len(offerIDs) == 0 {
		fmt.Println("nothing to offer: no installed extension is allowlisted.")
		fmt.Println("opt in with `gusset allow <id>` or --extensions <id>[,...] (see `gusset doctor` for IDs).")
		fmt.Println("continuing to listen so a peer can still pull nothing — this is a no-op.")
	} else {
		sort.Strings(offerIDs)
		fmt.Printf("offering %d extension(s): %s\n", len(offerIDs), strings.Join(offerIDs, ", "))
	}

	workDir, err := os.MkdirTemp("", "gusset-sync-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	offer, localCat, err := converge.BuildOffer(store.NewFirefox(profDir), k, pol, offerIDs, workDir, time.Now().Unix())
	if err != nil {
		return err
	}

	id, err := transport.DeriveIdentity(k)
	if err != nil {
		return err
	}
	model := status.New()
	// Stream progress to the terminal as it happens. Without this the model is
	// only surfaced live over --ws (for the companion extension); a bare CLI run
	// would otherwise show nothing until the end-of-run grid — which, with --for,
	// is after the whole reachable window elapses.
	model.SetEcho(os.Stderr)
	// served fires when a peer completes a clean pull from our listener, so a
	// one-shot --peer run can exit the moment the peer has our update instead of
	// lingering the full --for window.
	served := newServedSignal()
	srv, err := transport.Listen("tcp", *listenAddr, id, offer,
		transport.WithConnErrorHandler(func(ce transport.ConnError) {
			if ce.Phase == transport.PhaseHandshake {
				fmt.Fprintf(os.Stderr, "rejected an unauthenticated peer from %v\n", ce.Remote)
			}
		}),
		transport.WithServeComplete(func(_ net.Addr) {
			served.fire()
			model.Log(time.Now().Unix(), status.LogOK, "a peer pulled from you")
		}))
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	go func() { _ = srv.Serve() }()

	host, _ := os.Hostname()
	if host == "" {
		host = "gusset-peer"
	}
	// Resolve this device's stable unique id and friendly name (generated and
	// persisted on first use), with optional flag overrides. The unique id is
	// what keeps same-hostname devices from colliding.
	if changed, ierr := cfg.EnsureIdentity(host); ierr != nil {
		return ierr
	} else if changed {
		if serr := cfg.Save(); serr != nil {
			fmt.Fprintf(os.Stderr, "couldn't persist device identity: %v\n", serr)
		}
	}
	devID := cfg.DeviceID
	if *deviceID != "" {
		devID = *deviceID
	}
	devName := cfg.DeviceName
	if *deviceName != "" {
		devName = *deviceName
	}
	model.SetSelf(devID, devName)
	// deviceNamePtr is the live device name: it seeds the beacon each pass and is
	// updated atomically when the UI renames the device (set-name over the WS).
	var deviceNamePtr atomic.Pointer[string]
	deviceNamePtr.Store(&devName)

	tcpAddr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("unexpected listener address type %T", srv.Addr())
	}
	// mDNS advertises the unique device id (never the friendly name — the LAN
	// broadcast stays minimal); the id is what distinguishes same-hostname peers.
	stopAdvert, err := discovery.Advertise(devID, tcpAddr.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mDNS advertise unavailable (%v); peers must use --peer\n", err)
	} else {
		defer stopAdvert()
	}

	ctx := lifetimeContext(*watch, *forDur)
	var wsServer *statusws.Server
	if *wsAddr != "" {
		wsServer, err = startStatusWS(ctx, *wsAddr, model, k)
		if err != nil {
			return err
		}
		// Let the dashboard rename this device: persist it, update the live "this
		// device" label, and refresh the name future beacons carry. mDNS is
		// unaffected (it never broadcasts the friendly name), so no re-advertise.
		wsServer.OnSetName(func(name string) {
			deviceNamePtr.Store(&name)
			model.SetSelf(devID, name)
			cfg.DeviceName = name
			if serr := cfg.Save(); serr != nil {
				fmt.Fprintf(os.Stderr, "couldn't persist new device name: %v\n", serr)
			}
			model.Log(time.Now().Unix(), status.LogInfo, "renamed this device to "+name)
		})
	}
	target := store.NewFirefox(profDir)
	pullDeps := pullContext{id: id, target: target, k: k, localCat: localCat, allow: allow, workDir: workDir, model: model, force: *force, offer: offer, deviceName: &deviceNamePtr}

	var outcomes []converge.Outcome
	switch {
	case *peerAddr != "":
		if *rzDir != "" {
			fmt.Fprintln(os.Stderr, "note: --peer is set, so --rendezvous-dir is ignored (dialing the peer directly).")
		}
		outcomes, _ = pullFrom([]dialTarget{{addr: *peerAddr, link: status.LinkLAN}}, *peerAddr, "", pullDeps)
		reachableBackWait(ctx, *watch, *once, *forDur, outcomes, served)
	default:
		// The cross-network beacon carrier is a shared folder (--rendezvous-dir)
		// or, when the companion extension is connected over the WS, the
		// extension's storage.sync (the WS server is itself a rendezvous.Signaling).
		var carrier rendezvous.Signaling
		carrierLabel := ""
		switch {
		case *rzDir != "":
			carrier = rendezvous.DirSignaling{Dir: *rzDir}
			carrierLabel = *rzDir
		case wsServer != nil:
			carrier = wsServer
			carrierLabel = "the companion extension (storage.sync)"
		}
		rzSrc, beacon, sess := setupRendezvous(ctx, carrier, carrierLabel, devID, devName, *stunServer, host, tcpAddr.Port, k)
		if sess != nil {
			defer func() { _ = sess.Close() }() // releases the ICE agent if it was never consumed
		}
		pullDeps.selfID = devID
		pullDeps.iceSession = sess
		outcomes = runDiscoveryLoop(ctx, devID, pullDeps, rzSrc, beacon)
	}

	fmt.Println()
	status.Render(os.Stdout, model.Snapshot(), time.Now().Unix())
	applyBanner(outcomes)
	return nil
}

// relaunchFirefox restarts Firefox after a --restart-firefox run; on failure it
// tells the user to reopen it themselves (their session restores either way).
func relaunchFirefox(bin string) {
	if err := ffctl.Launch(bin); err != nil {
		fmt.Fprintf(os.Stderr, "couldn't relaunch Firefox (%v); reopen it yourself — your session restores.\n", err)
		return
	}
	fmt.Println("Relaunched Firefox.")
}

// applyBanner prints the action the user must take after a run. Firefox loads
// storage.local at startup, so applied settings need a restart to take effect,
// and a profile that was running could not be written at all — both are stated
// plainly rather than left implicit in the status grid.
func applyBanner(outcomes []converge.Outcome) {
	var applied, locked int
	for _, o := range outcomes {
		switch o.Action {
		case converge.Applied:
			applied++
		case converge.Locked:
			locked++
		}
	}
	if applied > 0 {
		fmt.Printf("\n✓ Applied new settings for %d extension(s) on this machine.\n"+
			"  Restart Firefox here to load them — they are on disk but not yet live.\n", applied)
	}
	if locked > 0 {
		fmt.Printf("\n⚠ Firefox is running, so %d extension(s) could not be applied.\n"+
			"  Close Firefox and re-run, or re-run with --restart-firefox to do it automatically.\n", locked)
	}
}

// pullContext bundles what pullFrom needs, to keep the signature small.
type pullContext struct {
	id       *transport.Identity
	target   *store.Firefox
	k        *crypto.Keys
	localCat converge.Catalog
	allow    func(string) bool
	workDir  string
	model    *status.Model
	force    bool // take the peer's copy unconditionally (seed/clone)

	// Tier-1 hole-punch fallback (set only on the rendezvous path). offer is
	// this device's chunk source, served back to the peer over the punched path;
	// iceSession is the gathered ICE agent (single-use per run); selfID is this
	// device's beacon id, used to break the ICE controlling tie deterministically.
	offer      transport.ChunkSource
	iceSession *icewire.Session
	selfID     string
	// deviceName is the live friendly name, re-read into the beacon each pass so
	// a UI rename propagates to peers without a restart.
	deviceName *atomic.Pointer[string]
}

// dialTarget is one candidate address for a peer, tagged with the link kind it
// represents so a successful connection records the right Link in status (a LAN
// endpoint vs a direct-NAT reflexive candidate).
type dialTarget struct {
	addr string
	link status.Link
}

// runDiscoveryLoop finds peers until the lifetime ends, pulling from each
// newly-seen peer once, and returns the accumulated reconcile outcomes. It
// browses mDNS on the LAN and, when rz is non-nil, also re-publishes this
// device's beacon and fetches peers' beacons from the Tier-1 carrier each pass —
// the two peer sources merge into one set, deduplicated so a peer reachable both
// ways is pulled once. The server keeps running throughout, so peers can pull
// from us concurrently.
func runDiscoveryLoop(ctx context.Context, selfID string, deps pullContext, rz *rendezvousSource, beacon rendezvous.Beacon) []converge.Outcome {
	var all []converge.Outcome
	// handled keys are device ids (the status-model peer key), so a peer reachable
	// both by mDNS and through the carrier is pulled once. lastSeen records when
	// each device was last advertised (mDNS browse or a fresh beacon), so a peer
	// that stops advertising can be pruned — independently of the pull-once map,
	// which never expires on its own.
	handled := map[string]bool{}
	lastSeen := map[string]int64{}
	for ctx.Err() == nil {
		now := time.Now().Unix()
		peers, err := discovery.Browse(ctx, selfID, browseGrace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "discovery: %v\n", err)
		}
		for _, p := range peers {
			lastSeen[p.Instance] = now
			if handled[p.Instance] {
				continue
			}
			handled[p.Instance] = true
			// On the LAN only the unique id is known (no friendly name is
			// broadcast), so it is both key and label.
			out, _ := pullFrom([]dialTarget{{addr: p.Addr, link: status.LinkLAN}}, p.Instance, "", deps)
			all = append(all, out...)
		}
		if rz != nil {
			all = append(all, rendezvousPass(ctx, rz, beacon, handled, lastSeen, now, deps)...)
		}
		// Drop devices that have stopped advertising — a finished test run, a
		// machine taken offline — so the peer list reflects who is reachable now,
		// not everyone ever seen. A still-present peer refreshes its lastSeen
		// every pass, and the window is far larger than one pass, so a single
		// missed mDNS browse never flickers a live peer out. A pruned device that
		// returns is re-pulled, because its handled entry is cleared too.
		for _, id := range stalePeerIDs(lastSeen, now, int64(peerStaleWindow/time.Second)) {
			deps.model.RemovePeer(id)
			delete(lastSeen, id)
			delete(handled, id)
		}
	}
	return all
}

// stalePeerIDs returns the device ids in lastSeen not re-advertised within window
// seconds (now - seen > window). Pure, so the prune decision is unit-testable
// without standing up discovery or a carrier.
func stalePeerIDs(lastSeen map[string]int64, now, window int64) []string {
	var stale []string
	for id, seen := range lastSeen {
		if now-seen > window {
			stale = append(stale, id)
		}
	}
	return stale
}

// rendezvousPass re-publishes this device's beacon (so its IssuedAt stays fresh
// while the window is open) and pulls from each newly-seen Tier-1 peer.
func rendezvousPass(ctx context.Context, rz *rendezvousSource, beacon rendezvous.Beacon, handled map[string]bool, lastSeen map[string]int64, now int64, deps pullContext) []converge.Outcome {
	beacon.IssuedAt = now
	if deps.deviceName != nil {
		beacon.Name = *deps.deviceName.Load() // pick up a UI rename without a restart
	}
	if err := rz.publish(ctx, beacon); err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: re-publish: %v\n", err)
	}
	rzPeers, err := rz.peers(ctx, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: fetch: %v\n", err)
		return nil
	}
	var out []converge.Outcome
	for _, p := range rzPeers {
		lastSeen[p.deviceID] = now // advertised this pass, so it is not pruned as stale
		if handled[p.deviceID] {
			continue
		}
		handled[p.deviceID] = true
		outcomes, dialed := pullFrom(p.targets, p.deviceID, p.name, deps)
		// Every direct target failed but the peer published a hole-punch
		// endpoint and we have an ICE agent to spend — try to punch through.
		if !dialed && p.ice != nil && deps.iceSession != nil {
			outcomes = icePull(p, deps)
		}
		out = append(out, outcomes...)
	}
	return out
}

// dialFirst tries each candidate in order and returns the first connection,
// along with the link kind of the candidate that answered. Order is the caller's
// preference (LAN before reflexive).
func dialFirst(targets []dialTarget, id *transport.Identity) (*transport.Client, status.Link, error) {
	if len(targets) == 0 {
		return nil, "", errors.New("no candidate addresses")
	}
	var lastErr error
	for _, t := range targets {
		client, err := transport.Dial("tcp", t.addr, id)
		if err == nil {
			return client, t.link, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

// pullFrom dials a peer (trying its candidates in order), reconciles, records the
// outcome in the status model (so the end-of-run grid explains every extension
// and peer), and returns the outcomes for the apply-action banner. The bool
// reports whether a connection was established: false means every direct target
// failed, so the caller may fall back to a hole-punch.
// pullFrom dials a peer identified by peerID (the unique device id used as the
// status key) with peerName as its friendly display label (may be empty, e.g.
// for LAN peers, in which case the id is shown).
func pullFrom(targets []dialTarget, peerID, peerName string, deps pullContext) ([]converge.Outcome, bool) {
	now := time.Now().Unix()
	label := peerName
	if label == "" {
		label = peerID
	}
	deps.model.Log(now, status.LogInfo, "dialing "+label)
	client, link, err := dialFirst(targets, deps.id)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: peerID, Name: peerName, State: status.Unreachable,
			Reason: status.PeerOffline, Detail: err.Error(), Since: now,
		})
		deps.model.Log(now, status.LogWarn, "couldn't reach "+label)
		return nil, false
	}
	defer func() { _ = client.Close() }()
	deps.model.SetPeer(status.Peer{DeviceID: peerID, Name: peerName, State: status.Connected, Link: link, Since: now})
	deps.model.Log(now, status.LogInfo, "connected to "+label+" over "+string(link))

	outcomes, err := converge.Pull(client, deps.target, deps.k, deps.localCat, deps.allow, deps.workDir, deps.force)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: peerID, Name: peerName, State: status.Unreachable,
			Reason: status.AuthFailed, Detail: err.Error(), Since: now,
		})
		deps.model.Log(now, status.LogError, "sync with "+label+" failed")
		return nil, true // connected, but the reconcile failed — not a dial failure
	}
	for _, o := range outcomes {
		deps.model.SetExtSync(toExtSync(o, peerID, now))
		if level, msg, ok := outcomeLogLine(o, label); ok {
			deps.model.Log(now, level, msg)
		}
	}
	return outcomes, true
}

// outcomeLogLine maps a reconcile Outcome onto an activity-log line, so a tester
// can see why a sync did (or didn't) change anything. It returns ok=false for
// outcomes that should not be logged — LocalNewer, the common no-op, is kept out
// so it can't flood the bounded ring. Only the extension id and the peer label
// (both already shown in the UI) ever appear; never data values. label is the
// peer's display label as computed in pullFrom.
func outcomeLogLine(o converge.Outcome, label string) (status.LogLevel, string, bool) {
	switch o.Action {
	case converge.Applied:
		return status.LogOK, "applied " + o.Extension + " from " + label, true
	case converge.Locked:
		return status.LogWarn, "fetched " + o.Extension + " from " + label + " — close Firefox to apply", true
	case converge.Blocked:
		return status.LogWarn, o.Extension + " offered by " + label + " is not allowed here — run gusset allow", true
	case converge.Failed:
		return status.LogError, "couldn't sync " + o.Extension + " from " + label, true
	default: // LocalNewer and any future no-op action
		return "", "", false
	}
}

// toExtSync maps a reconcile Outcome onto a status entry.
func toExtSync(o converge.Outcome, peer string, now int64) status.ExtSync {
	e := status.ExtSync{Extension: o.Extension, DeviceID: peer, Since: now, Detail: o.Detail}
	switch o.Action {
	case converge.Applied:
		// On disk but not live until Firefox restarts.
		e.State = status.Pending
		e.Detail = "restart Firefox to load"
	case converge.LocalNewer:
		e.State = status.InSync
	case converge.Blocked:
		e.State = status.Blocked
	case converge.Locked:
		// Fetched and verified, just not applied — Firefox is running.
		e.State = status.Pending
		e.Detail = "close Firefox and re-run to apply"
	default:
		e.State = status.Errored
	}
	return e
}

// setupRendezvous prepares the cross-network peer feed when a beacon carrier is
// available (a shared folder, or the companion extension over the WS): it
// gathers this device's beacon (LAN endpoints, plus an ICE hole-punch endpoint
// when --stun is given), publishes it once so a peer already waiting sees us
// immediately, and returns the live ICE session for the punch fallback. It
// returns nils (LAN-only) when no carrier is configured or the first publish
// fails. label names the carrier for the log line. devID is the unique id;
// devName is the friendly name carried (sealed) in the beacon.
func setupRendezvous(ctx context.Context, carrier rendezvous.Signaling, label, devID, devName, stunServer, host string, port int, k *crypto.Keys) (*rendezvousSource, rendezvous.Beacon, *icewire.Session) {
	if carrier == nil {
		return nil, rendezvous.Beacon{}, nil
	}
	src := &rendezvousSource{sig: carrier, k: k, selfID: devID}
	beacon := gatherBeacon(devID, host, devName, port, time.Now().Unix())

	// A STUN server enables the hole-punch fallback: gather an ICE endpoint and
	// advertise it so a peer we can't dial directly can punch to us.
	var sess *icewire.Session
	if stunServer != "" {
		sess, beacon.ICE = gatherICESession(stunServer)
	}

	if err := src.publish(ctx, beacon); err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: publish beacon (%v); continuing LAN-only.\n", err)
		if sess != nil {
			_ = sess.Close()
		}
		return nil, rendezvous.Beacon{}, nil
	}
	cands := len(beacon.LANEndpoints)
	punch := ""
	if beacon.ICE != nil {
		punch = ", hole-punch enabled"
	}
	fmt.Printf("rendezvous: published beacon %q with %d candidate endpoint(s)%s via %s\n", devID, cands, punch, label)
	return src, beacon, sess
}

// reachableBackWait decides whether a one-shot --peer run lingers after its pull.
// The pull is already done; the only reason to stay reachable is so the peer can
// pull *our* newer data back. So: --watch stays until Ctrl-C; --once exits now;
// otherwise we linger only when we actually hold something the peer would want,
// and exit immediately when everything is already in sync — the common seed case,
// where we took the peer's copy and hold nothing newer.
//
// When we do hold newer data, served lets us exit the instant the peer finishes
// pulling it (the listener fires it on a completed pull), with --for as the upper
// bound if the peer never comes.
func reachableBackWait(ctx context.Context, watch, once bool, forDur time.Duration, outcomes []converge.Outcome, served *servedSignal) {
	switch {
	case watch:
		fmt.Fprintln(os.Stderr, "→ pull done — staying reachable until Ctrl-C so the peer can sync back.")
		<-ctx.Done()
	case once:
		fmt.Fprintln(os.Stderr, "✓ pull done — exiting now (--once).")
	case holdsUpdatesToOffer(outcomes):
		waitForPullback(ctx, forDur, served)
	default:
		fmt.Fprintln(os.Stderr, "✓ everything in sync — nothing for the peer to pull back; exiting.")
	}
}

// waitForPullback lingers after a pull that left us holding newer data, exiting
// the moment the peer pulls it back (served) rather than burning the whole --for
// window. --for stays the fallback for when the peer never returns.
func waitForPullback(ctx context.Context, forDur time.Duration, served *servedSignal) {
	if served.served() { // a peer already pulled before we reached the linger
		fmt.Fprintln(os.Stderr, "✓ pull done — the peer already pulled your update back; exiting.")
		return
	}
	fmt.Fprintf(os.Stderr, "→ pull done — you hold newer data; staying reachable until the peer pulls it back (up to %s; Ctrl-C to quit now).\n", forDur)
	select {
	case <-served.ch:
		fmt.Fprintln(os.Stderr, "✓ the peer pulled your update back — exiting.")
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "→ reachable window elapsed before the peer pulled back — exiting.")
	}
}

// servedSignal is a one-way notice that a peer completed a clean pull from this
// device's listener. fire is safe to call from the listener's per-connection
// goroutines; waitForPullback consumes it. The buffered channel coalesces
// wakeups, and the counter lets a waiter that arrives after the pull see it.
type servedSignal struct {
	n  atomic.Int64
	ch chan struct{}
}

func newServedSignal() *servedSignal {
	return &servedSignal{ch: make(chan struct{}, 1)}
}

func (s *servedSignal) fire() {
	s.n.Add(1)
	select {
	case s.ch <- struct{}{}:
	default: // a wakeup is already pending; one is enough
	}
}

func (s *servedSignal) served() bool { return s.n.Load() > 0 }

// holdsUpdatesToOffer reports whether our reconcile left us holding a copy newer
// than the peer's for some extension — the only case where staying reachable
// benefits the peer (it can pull our copy). A LocalNewer outcome means exactly
// that, modulo an exact same-timestamp tie, where a brief linger is harmless.
func holdsUpdatesToOffer(outcomes []converge.Outcome) bool {
	for _, o := range outcomes {
		if o.Action == converge.LocalNewer {
			return true
		}
	}
	return false
}

// lifetimeContext builds the run's deadline: indefinite under --watch (until a
// signal), else bounded by --for. SIGINT/SIGTERM always cancel.
func lifetimeContext(watch bool, forDur time.Duration) context.Context {
	base := context.Background()
	var ctx context.Context
	var cancel context.CancelFunc
	if watch {
		ctx, cancel = context.WithCancel(base)
	} else {
		ctx, cancel = context.WithTimeout(base, forDur)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()
	return ctx
}

// buildPolicy merges the persisted config allowlist/overrides with any
// --extensions / --override flags (flags are additive for one-off syncs).
func buildPolicy(cfg *config.Config, extCSV, overrideCSV string) *policy.Policy {
	pl := policy.New()
	for _, id := range cfg.Allowlist {
		pl.Allow(id)
	}
	for _, id := range cfg.Overrides {
		pl.Override(id)
	}
	for _, id := range splitCSV(extCSV) {
		pl.Allow(id)
	}
	for _, id := range splitCSV(overrideCSV) {
		pl.Override(id)
	}
	return pl
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// localProfile resolves a Firefox profile dir and its installed extension UUIDs.
// An explicit override (the --profile flag or GUSSET_PROFILE) is used as-is —
// useful for testing two profiles on one machine; otherwise the active default
// profile is resolved.
func localProfile(override string) (dir string, installed map[string]string, err error) {
	dir = override
	if dir == "" {
		root, rerr := profile.FirefoxRoot()
		if rerr != nil {
			return "", nil, rerr
		}
		dir, err = profile.DefaultProfileDir(root)
		if err != nil {
			return "", nil, err
		}
	}
	installed, err = profile.ExtensionUUIDs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("reading profile %s: %w", dir, err)
	}
	return dir, installed, nil
}

// profileOverride returns the explicit profile dir to use: the flag value, else
// GUSSET_PROFILE, else "" (auto-resolve the active profile).
func profileOverride(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("GUSSET_PROFILE")
}

// readPassphrase loads the root secret, in order of preference: the config's
// passphrase_file, then the default file (<config-dir>/passphrase), then
// GUSSET_PASSPHRASE_FILE, then GUSSET_PASSPHRASE. A file is preferred over the
// environment because it keeps the secret out of `ps`/the process environment.
func readPassphrase(cfg *config.Config) (string, error) {
	candidates := []string{cfg.PassphraseFile}
	if d, err := config.Dir(); err == nil {
		candidates = append(candidates, filepath.Join(d, "passphrase"))
	}
	candidates = append(candidates, os.Getenv("GUSSET_PASSPHRASE_FILE"))
	for _, path := range candidates {
		if path == "" {
			continue
		}
		info, err := os.Stat(path) //nolint:gosec // G703: path is a locally configured passphrase-file location, not remote input
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read passphrase file %s: %w", path, err)
		}
		// The passphrase file is the root secret. Refuse it if it is readable by
		// group or other — a lax umask or a careless copy should fail loudly, not
		// hand the secret to every local account. Unix mode bits are meaningless on
		// Windows (os.Stat reports 0666 regardless; access is governed by ACLs), so
		// the guard there would reject every passphrase file — skip it.
		if runtime.GOOS != "windows" {
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				return "", fmt.Errorf("passphrase file %s is too permissive (mode %04o); run `chmod 600 %s`", path, perm, path)
			}
		}
		b, err := os.ReadFile(path) //nolint:gosec // G703: same locally configured passphrase-file path
		if err != nil {
			return "", fmt.Errorf("read passphrase file %s: %w", path, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if p := os.Getenv("GUSSET_PASSPHRASE"); p != "" {
		return p, nil
	}
	return "", errors.New("no passphrase: run `gusset init`, write <config-dir>/passphrase, or set GUSSET_PASSPHRASE")
}
