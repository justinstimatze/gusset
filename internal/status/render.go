package status

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// noReason is the loud fallback for a non-converged state that arrived without
// an explanation. The design rule is "never sync silently": a missing reason is
// a bug, so we render it conspicuously rather than as a blank — surfaced, not
// hidden.
const noReason = "(no reason recorded — please report)"

// PeerWhy returns the human explanation for a peer's current state, or "" when
// the peer is Connected (the only self-evidently-fine state). Transitional
// states explain themselves; an Unreachable peer must carry a Reason, and a
// missing one renders loudly.
func PeerWhy(p Peer) string {
	switch p.State {
	case Connected:
		return ""
	case Unreachable:
		if p.Reason == "" {
			return withDetail(noReason, p.Detail)
		}
		return withDetail(string(p.Reason), p.Detail)
	default: // Discovering, Signaling, HolePunching — the state is the reason
		return withDetail("", p.Detail)
	}
}

// ExtWhy returns the human explanation for an extension's sync state with a
// peer, or "" when it is in sync. Every non-converged state yields a non-empty
// string; an unexplained one renders loudly.
func ExtWhy(e ExtSync) string {
	switch e.State {
	case InSync:
		return ""
	case Pushing:
		return withDetail(strconv.Itoa(e.Remaining)+" chunks left to push", e.Detail)
	case Pulling:
		return withDetail(strconv.Itoa(e.Remaining)+" chunks left to pull", e.Detail)
	case Stale:
		return withDetail("peer offline", e.Detail)
	case Pending:
		if e.Detail == "" {
			return "restart Firefox to apply"
		}
		return e.Detail
	case Blocked:
		if e.Detail == "" {
			return "denylisted (override to sync anyway)"
		}
		return e.Detail
	case Errored:
		if e.Detail == "" {
			return noReason
		}
		return e.Detail
	default:
		return withDetail(noReason, e.Detail)
	}
}

// withDetail joins a base reason with optional free-text detail.
func withDetail(base, detail string) string {
	switch {
	case base == "" && detail == "":
		return ""
	case base == "":
		return detail
	case detail == "":
		return base
	default:
		return base + " — " + detail
	}
}

// Render writes a human-readable status report. now (unix secs) is supplied so
// "since" durations are deterministic; the CLI passes time.Now().Unix(). The
// empty case is rendered explicitly — silence is never the answer, including
// "nothing is configured yet."
func Render(w io.Writer, snap Snapshot, now int64) {
	fmt.Fprintln(w, "peers:")
	if len(snap.Peers) == 0 {
		fmt.Fprintln(w, "  none paired yet")
	}
	for _, p := range snap.Peers {
		name := p.Name
		if name == "" {
			name = p.DeviceID
		}
		state := string(p.State)
		if p.State == Connected && p.Link != "" {
			state += " (" + string(p.Link) + ")"
		}
		if why := PeerWhy(p); why != "" {
			state += ": " + why
		}
		// name comes from a peer's beacon/mDNS label and state may fold in
		// peer-supplied Detail — sanitize before it reaches the terminal.
		fmt.Fprintf(w, "  %-20s %-28s %s\n", sanitize(name), sanitize(state), ago(now, p.Since))
	}

	fmt.Fprintln(w, "extensions:")
	if len(snap.Extensions) == 0 {
		fmt.Fprintln(w, "  none allowlisted yet (gusset syncs nothing until you opt one in)")
	}
	for _, e := range snap.Extensions {
		state := string(e.State)
		if why := ExtWhy(e); why != "" {
			state += ": " + why
		}
		// Extension id and peer id arrive over the wire; state may fold in
		// peer-supplied Detail — sanitize each before the terminal sees it.
		fmt.Fprintf(w, "  %-44s -> %-16s %-32s %s\n", sanitize(e.Extension), sanitize(e.DeviceID), sanitize(state), ago(now, e.Since))
	}
}

// sanitize neutralizes a remote-supplied string before it is written to a
// terminal: any non-printable rune (ANSI escapes, carriage returns, bells, other
// control or format characters a peer could embed in a beacon label or detail
// string) becomes the visible replacement character, so tampering shows up rather
// than executing as a terminal control sequence. Ordinary printable Unicode
// (including non-ASCII hostnames) passes through unchanged.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '�'
	}, s)
}

// ago renders a coarse "Xs/Xm/Xh/Xd ago" for a unix-seconds timestamp. A zero or
// future since renders as "just now" rather than a negative or epoch duration.
func ago(now, since int64) string {
	if since <= 0 || now <= since {
		return "just now"
	}
	d := time.Duration(now-since) * time.Second
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
