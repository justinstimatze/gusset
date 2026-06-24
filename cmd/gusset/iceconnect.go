package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/justinstimatze/gusset/internal/converge"
	"github.com/justinstimatze/gusset/internal/icewire"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
	"github.com/justinstimatze/gusset/internal/transport"
)

const (
	// iceGatherTimeout bounds the one-time STUN gather at startup.
	iceGatherTimeout = 8 * time.Second
	// iceConnectTimeout bounds a single hole-punch attempt plus its QUIC handshake
	// and the reconcile that follows, so a half-open peer cannot stall the run.
	iceConnectTimeout = 45 * time.Second
)

// gatherICESession gathers this device's ICE candidates once and returns the
// live session (for the later hole-punch) plus the endpoint to advertise in the
// beacon. A gather failure is non-fatal: it just disables the fallback for the
// run (LAN, reflexive, and the direct paths still work).
func gatherICESession(stunServer string) (*icewire.Session, *rendezvous.ICEEndpoint) {
	ctx, cancel := context.WithTimeout(context.Background(), iceGatherTimeout)
	defer cancel()
	sess, err := icewire.Gather(ctx, icewire.Config{STUNURLs: []string{"stun:" + stunServer}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ice: gather (%v); hole-punch fallback disabled this run\n", err)
		return nil, nil
	}
	ep := sess.Local()
	return sess, &rendezvous.ICEEndpoint{Ufrag: ep.Ufrag, Pwd: ep.Pwd, Candidates: ep.Candidates}
}

// icePull is the fallback when every direct target failed: punch a hole to the
// peer with ICE, then reconcile over the punched path. The ICE session is
// single-use, so this is spent on the first peer that needs it in a run.
func icePull(p rzPeer, deps pullContext) []converge.Outcome {
	now := time.Now().Unix()
	deps.model.SetPeer(status.Peer{DeviceID: p.deviceID, Name: p.name, State: status.HolePunching, Since: now})

	ctx, cancel := context.WithTimeout(context.Background(), iceConnectTimeout)
	defer cancel()

	// Exactly one side controls ICE (and is the QUIC client); the greater device
	// id wins, so both peers independently agree on opposite roles. Identical ids
	// would make both controlled — the persisted unique device ids avoid that.
	controlling := deps.selfID > p.deviceID

	conn, err := deps.iceSession.Connect(ctx, deps.id, toICEEndpoint(*p.ice), controlling)
	if err != nil {
		deps.model.SetPeer(status.Peer{
			DeviceID: p.deviceID, Name: p.name, State: status.Unreachable,
			Reason: status.NATFailed, Detail: err.Error(), Since: now,
		})
		return nil
	}
	defer func() { _ = conn.Close() }()
	deps.model.SetPeer(status.Peer{DeviceID: p.deviceID, Name: p.name, State: status.Connected, Link: status.LinkDirectNAT, Since: now})

	return iceReconcile(ctx, conn, p.deviceID, deps)
}

// iceReconcile runs a full bidirectional reconcile over one punched QUIC
// connection: we serve our offer to the peer on the stream it opens, and pull
// the peer's chunks on a stream we open. A punched path is expensive to
// establish, so a single connection carries both directions rather than the
// two separate connections the LAN path uses.
func iceReconcile(ctx context.Context, conn *icewire.Conn, name string, deps pullContext) []converge.Outcome {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return // peer never pulled (or ctx ended) — nothing to serve
		}
		_ = transport.Serve(stream, deps.offer) // returns nil on the peer's clean hangup
	}()

	var outcomes []converge.Outcome
	stream, err := conn.OpenStream(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ice reconcile (%s): open stream: %v\n", name, err)
		wg.Wait()
		return nil
	}
	client := transport.NewClient(stream)
	outcomes, perr := converge.Pull(client, deps.target, deps.k, deps.localCat, deps.allow, deps.workDir)
	_ = client.Close() // FIN our pull stream so the peer's serve loop ends
	wg.Wait()
	if perr != nil {
		fmt.Fprintf(os.Stderr, "ice reconcile (%s): %v\n", name, perr)
		return nil
	}

	at := time.Now().Unix()
	for _, o := range outcomes {
		deps.model.SetExtSync(toExtSync(o, name, at))
	}
	return outcomes
}

// toICEEndpoint converts the beacon's wire-format ICE endpoint into the
// icewire type (the two are deliberately separate so rendezvous stays free of
// the pion dependency).
func toICEEndpoint(e rendezvous.ICEEndpoint) icewire.Endpoint {
	return icewire.Endpoint{Ufrag: e.Ufrag, Pwd: e.Pwd, Candidates: e.Candidates}
}
