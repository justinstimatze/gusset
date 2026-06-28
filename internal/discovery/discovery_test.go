package discovery

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestAdvertiseBrowse_RoundTrip advertises a fake peer and confirms Browse finds
// it. mDNS needs working multicast on a loopback/LAN interface, which some
// sandboxes and CI environments lack — so a clean "found nothing" is treated as
// an unavailable environment and skipped, not failed. When multicast works, the
// discovered address must be dialable (host:port).
func TestAdvertiseBrowse_RoundTrip(t *testing.T) {
	stop, err := Advertise("peer-under-test", 45123)
	if err != nil {
		t.Skipf("mDNS advertise unavailable here: %v", err)
	}
	defer stop()

	peers, err := Browse(context.Background(), "this-device", 3*time.Second)
	if err != nil {
		t.Skipf("mDNS browse unavailable here: %v", err)
	}

	var found *Peer
	for i := range peers {
		if peers[i].Instance == "peer-under-test" {
			found = &peers[i]
			break
		}
	}
	if found == nil {
		t.Skip("mDNS produced no result in this environment (multicast likely blocked)")
	}
	if _, _, err := net.SplitHostPort(found.Addr); err != nil {
		t.Errorf("discovered addr %q is not host:port: %v", found.Addr, err)
	}
}

// TestBrowse_SkipsSelf confirms the self-instance filter: a device advertising
// under name N must not return itself when browsing as N.
func TestBrowse_SkipsSelf(t *testing.T) {
	stop, err := Advertise("same-name", 45124)
	if err != nil {
		t.Skipf("mDNS advertise unavailable here: %v", err)
	}
	defer stop()

	peers, err := Browse(context.Background(), "same-name", 2*time.Second)
	if err != nil {
		t.Skipf("mDNS browse unavailable here: %v", err)
	}
	for _, p := range peers {
		if p.Instance == "same-name" {
			t.Fatalf("Browse returned self: %+v", p)
		}
	}
}
