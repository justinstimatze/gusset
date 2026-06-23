package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/converge"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/discovery"
	"github.com/justinstimatze/gusset/internal/policy"
	"github.com/justinstimatze/gusset/internal/profile"
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
	extCSV := fs.String("extensions", "", "comma-separated extension IDs to sync (the allowlist)")
	overrideCSV := fs.String("override", "", "comma-separated sensitive extension IDs to force-enable")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pass, err := readPassphrase()
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassphrase(pass); err != nil {
		return err
	}
	k, err := crypto.DeriveKeys(pass, crypto.AppSalt)
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
	pl := buildPolicy(*extCSV, *overrideCSV)
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
		fmt.Println("opt in with --extensions <id>[,<id>...] (see `gusset doctor` for installed IDs).")
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
	srv, err := transport.Listen("tcp", "0.0.0.0:0", id, offer,
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

	if *peerAddr != "" {
		pullFrom(*peerAddr, *peerAddr, pullDeps)
		<-ctx.Done() // linger so the peer can pull from us within the window
	} else {
		runDiscoveryLoop(ctx, host, pullDeps)
	}

	fmt.Println()
	status.Render(os.Stdout, model.Snapshot(), time.Now().Unix())
	return nil
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

// runDiscoveryLoop browses for peers until the lifetime ends, pulling from each
// newly-seen peer once. The server keeps running throughout, so peers can pull
// from us concurrently.
func runDiscoveryLoop(ctx context.Context, host string, deps pullContext) {
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
			pullFrom(p.Addr, p.Instance, deps)
		}
	}
}

// pullFrom dials one peer, reconciles, and records the outcome in the status
// model so the end-of-run summary explains every extension and peer.
func pullFrom(addr, name string, deps pullContext) {
	now := time.Now().Unix()
	client, err := transport.Dial("tcp", addr, deps.id)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: name, State: status.Unreachable,
			Reason: status.PeerOffline, Detail: err.Error(), Since: now,
		})
		return
	}
	defer func() { _ = client.Close() }()
	deps.model.SetPeer(status.Peer{DeviceID: name, State: status.Connected, Link: status.LinkLAN, Since: now})

	outcomes, err := converge.Pull(client, deps.target, deps.k, deps.localCat, deps.allow, deps.workDir)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: name, State: status.Unreachable,
			Reason: status.AuthFailed, Detail: err.Error(), Since: now,
		})
		return
	}
	for _, o := range outcomes {
		deps.model.SetExtSync(toExtSync(o, name, now))
	}
}

// toExtSync maps a reconcile Outcome onto a status entry.
func toExtSync(o converge.Outcome, peer string, now int64) status.ExtSync {
	e := status.ExtSync{Extension: o.Extension, DeviceID: peer, Since: now, Detail: o.Detail}
	switch o.Action {
	case converge.Applied, converge.LocalNewer:
		e.State = status.InSync
	case converge.Blocked:
		e.State = status.Blocked
	default:
		e.State = status.Errored
	}
	return e
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

// buildPolicy turns the --extensions / --override flags into a Policy.
func buildPolicy(extCSV, overrideCSV string) *policy.Policy {
	pl := policy.New()
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

// readPassphrase loads the root secret from GUSSET_PASSPHRASE_FILE (preferred —
// keeps it out of the process environment and `ps`) or GUSSET_PASSPHRASE.
func readPassphrase() (string, error) {
	if path := os.Getenv("GUSSET_PASSPHRASE_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read GUSSET_PASSPHRASE_FILE: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if p := os.Getenv("GUSSET_PASSPHRASE"); p != "" {
		return p, nil
	}
	return "", errors.New("set GUSSET_PASSPHRASE_FILE (a path) or GUSSET_PASSPHRASE (the 8-word secret)")
}
