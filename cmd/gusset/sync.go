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
	"strings"
	"syscall"
	"time"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/converge"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/discovery"
	"github.com/justinstimatze/gusset/internal/ffctl"
	"github.com/justinstimatze/gusset/internal/policy"
	"github.com/justinstimatze/gusset/internal/profile"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
	"github.com/justinstimatze/gusset/internal/store"
	"github.com/justinstimatze/gusset/internal/transport"
)

// browseGrace bounds one mDNS browse pass; the sync loop repeats it until the
// chosen lifetime elapses.
const browseGrace = 3 * time.Second

// syncCmd is the on-demand sync (docs/transport-and-security.md §8): it serves
// this device's allowlisted extensions and pulls a peer's newer ones over
// passphrase-authed mutual TLS, discovered on the LAN by mDNS. Its listener
// lifetime is the user's choice — one-shot window (default), bounded (--for), or
// indefinite (--watch) — so nobody is forced into an always-on process.
func syncCmd(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	forDur := fs.Duration("for", 30*time.Second, "stay reachable for this long, then exit")
	watch := fs.Bool("watch", false, "stay reachable indefinitely (until Ctrl-C)")
	peerAddr := fs.String("peer", "", "dial this host:port directly, skipping mDNS discovery")
	rzDir := fs.String("rendezvous-dir", "", "exchange sealed beacons through this shared folder to reach peers off the LAN (Tier 1)")
	deviceID := fs.String("device-id", "", "stable id for this device in rendezvous beacons (default: hostname)")
	stunServer := fs.String("stun", "", "STUN server host:port to learn this device's public IP for its beacon (e.g. stun.l.google.com:19302)")
	listenAddr := fs.String("listen", "0.0.0.0:0", "address to listen on (host:port; :0 picks a free port)")
	extCSV := fs.String("extensions", "", "comma-separated extension IDs to sync (the allowlist)")
	overrideCSV := fs.String("override", "", "comma-separated sensitive extension IDs to force-enable")
	restartFF := fs.Bool("restart-firefox", false, "close Firefox to apply, then relaunch it (destructive: closes your browser)")
	ffBin := fs.String("firefox-bin", "", "Firefox binary to relaunch with --restart-firefox (default: platform Firefox)")
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

	profDir, installed, err := localProfile()
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
	srv, err := transport.Listen("tcp", *listenAddr, id, offer,
		transport.WithConnErrorHandler(func(ce transport.ConnError) {
			if ce.Phase == transport.PhaseHandshake {
				fmt.Fprintf(os.Stderr, "rejected an unauthenticated peer from %v\n", ce.Remote)
			}
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
	tcpAddr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("unexpected listener address type %T", srv.Addr())
	}
	stopAdvert, err := discovery.Advertise(host, tcpAddr.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mDNS advertise unavailable (%v); peers must use --peer\n", err)
	} else {
		defer stopAdvert()
	}

	ctx := lifetimeContext(*watch, *forDur)
	target := store.NewFirefox(profDir)
	pullDeps := pullContext{id: id, target: target, k: k, localCat: localCat, allow: allow, workDir: workDir, model: model}

	var outcomes []converge.Outcome
	switch {
	case *peerAddr != "":
		if *rzDir != "" {
			fmt.Fprintln(os.Stderr, "note: --peer is set, so --rendezvous-dir is ignored (dialing the peer directly).")
		}
		outcomes = pullFrom([]dialTarget{{addr: *peerAddr, link: status.LinkLAN}}, *peerAddr, pullDeps)
		<-ctx.Done() // linger so the peer can pull from us within the window
	default:
		rzSrc, beacon := setupRendezvous(ctx, *rzDir, *deviceID, *stunServer, host, tcpAddr.Port, k)
		outcomes = runDiscoveryLoop(ctx, host, pullDeps, rzSrc, beacon)
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
func runDiscoveryLoop(ctx context.Context, host string, deps pullContext, rz *rendezvousSource, beacon rendezvous.Beacon) []converge.Outcome {
	var all []converge.Outcome
	handled := map[string]bool{}
	for ctx.Err() == nil {
		peers, err := discovery.Browse(ctx, host, browseGrace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "discovery: %v\n", err)
		}
		for _, p := range peers {
			if handled[p.Addr] {
				continue
			}
			handled[p.Addr] = true
			all = append(all, pullFrom([]dialTarget{{addr: p.Addr, link: status.LinkLAN}}, p.Instance, deps)...)
		}
		if rz != nil {
			all = append(all, rendezvousPass(ctx, rz, beacon, handled, deps)...)
		}
	}
	return all
}

// rendezvousPass re-publishes this device's beacon (so its IssuedAt stays fresh
// while the window is open) and pulls from each newly-seen Tier-1 peer.
func rendezvousPass(ctx context.Context, rz *rendezvousSource, beacon rendezvous.Beacon, handled map[string]bool, deps pullContext) []converge.Outcome {
	beacon.IssuedAt = time.Now().Unix()
	if err := rz.publish(ctx, beacon); err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: re-publish: %v\n", err)
	}
	rzPeers, err := rz.peers(ctx, time.Now().Unix())
	if err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: fetch: %v\n", err)
		return nil
	}
	var out []converge.Outcome
	for _, p := range rzPeers {
		key := "rz:" + p.deviceID
		if handled[key] {
			continue
		}
		handled[key] = true
		out = append(out, pullFrom(p.targets, p.instance, deps)...)
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
// and peer), and returns the outcomes for the apply-action banner.
func pullFrom(targets []dialTarget, name string, deps pullContext) []converge.Outcome {
	now := time.Now().Unix()
	client, link, err := dialFirst(targets, deps.id)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: name, State: status.Unreachable,
			Reason: status.PeerOffline, Detail: err.Error(), Since: now,
		})
		return nil
	}
	defer func() { _ = client.Close() }()
	deps.model.SetPeer(status.Peer{DeviceID: name, State: status.Connected, Link: link, Since: now})

	outcomes, err := converge.Pull(client, deps.target, deps.k, deps.localCat, deps.allow, deps.workDir)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: name, State: status.Unreachable,
			Reason: status.AuthFailed, Detail: err.Error(), Since: now,
		})
		return nil
	}
	for _, o := range outcomes {
		deps.model.SetExtSync(toExtSync(o, name, now))
	}
	return outcomes
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

// setupRendezvous prepares the Tier-1 peer feed when --rendezvous-dir is set:
// it gathers this device's beacon (LAN endpoints, plus a STUN reflexive
// candidate when --stun is given) and publishes it once so a peer already
// waiting sees us immediately. It returns (nil, zero) when no rendezvous dir is
// configured or the first publish fails — the caller then runs LAN-only.
func setupRendezvous(ctx context.Context, rzDir, deviceID, stunServer, host string, port int, k *crypto.Keys) (*rendezvousSource, rendezvous.Beacon) {
	if rzDir == "" {
		return nil, rendezvous.Beacon{}
	}
	devID := deviceID
	if devID == "" {
		devID = host
	}
	src := &rendezvousSource{sig: rendezvous.DirSignaling{Dir: rzDir}, k: k, selfID: devID}
	beacon := gatherBeacon(devID, host, port, stunServer, time.Now().Unix())
	if err := src.publish(ctx, beacon); err != nil {
		fmt.Fprintf(os.Stderr, "rendezvous: publish beacon (%v); continuing LAN-only.\n", err)
		return nil, rendezvous.Beacon{}
	}
	cands := len(beacon.LANEndpoints)
	if beacon.SrvReflexive != "" {
		cands++
	}
	fmt.Printf("rendezvous: published beacon %q with %d candidate endpoint(s) to %s\n", devID, cands, rzDir)
	return src, beacon
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

// localProfile resolves the active Firefox profile dir and its installed
// extension UUIDs.
func localProfile() (dir string, installed map[string]string, err error) {
	root, err := profile.FirefoxRoot()
	if err != nil {
		return "", nil, err
	}
	dir, err = profile.DefaultProfileDir(root)
	if err != nil {
		return "", nil, err
	}
	installed, err = profile.ExtensionUUIDs(dir)
	if err != nil {
		return "", nil, err
	}
	return dir, installed, nil
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
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
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
