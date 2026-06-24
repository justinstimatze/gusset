package statusws

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/status"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// wsURL turns an httptest server's http URL into a ws URL.
func wsURL(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http")
}

// dialAuthed dials, sends the given token, and returns the open connection.
func dialAuthed(t *testing.T, srv *httptest.Server, token string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := wsjson.Write(ctx, conn, authMsg{Token: token}); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	return conn, ctx
}

func readSnapshot(t *testing.T, ctx context.Context, conn *websocket.Conn) status.Snapshot {
	t.Helper()
	var msg statusMsg
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if msg.Type != "status" {
		t.Fatalf("unexpected message type %q", msg.Type)
	}
	return msg.Snapshot
}

func TestServer_AuthAndInitialSnapshot(t *testing.T) {
	m := status.New()
	m.SetPeer(status.Peer{DeviceID: "laptop", State: status.Connected, Link: status.LinkLAN})
	srv := httptest.NewServer(NewServer(m, testToken))
	defer srv.Close()

	conn, ctx := dialAuthed(t, srv, testToken)
	defer func() { _ = conn.CloseNow() }()

	snap := readSnapshot(t, ctx, conn)
	if len(snap.Peers) != 1 || snap.Peers[0].DeviceID != "laptop" {
		t.Fatalf("initial snapshot missing the peer: %+v", snap.Peers)
	}
}

func TestServer_RejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(NewServer(status.New(), testToken))
	defer srv.Close()

	conn, ctx := dialAuthed(t, srv, "the-wrong-token")
	defer func() { _ = conn.CloseNow() }()

	// The next read must fail: the server closes an unauthenticated socket
	// before sending any status, and it must not leak a snapshot.
	var msg statusMsg
	if err := wsjson.Read(ctx, conn, &msg); err == nil {
		t.Fatalf("server sent data to an unauthenticated client: %+v", msg)
	}
}

func TestServer_PushesOnChange(t *testing.T) {
	m := status.New()
	srv := httptest.NewServer(NewServer(m, testToken))
	defer srv.Close()

	conn, ctx := dialAuthed(t, srv, testToken)
	defer func() { _ = conn.CloseNow() }()

	// Initial snapshot is empty.
	if got := readSnapshot(t, ctx, conn); len(got.Peers) != 0 {
		t.Fatalf("expected empty initial snapshot, got %+v", got.Peers)
	}

	// A model change is pushed without the client asking.
	m.SetPeer(status.Peer{DeviceID: "phone", State: status.Discovering})
	snap := readSnapshot(t, ctx, conn)
	if len(snap.Peers) != 1 || snap.Peers[0].DeviceID != "phone" {
		t.Fatalf("change was not pushed: %+v", snap.Peers)
	}
}

func TestServer_RefusesNonLoopbackBind(t *testing.T) {
	err := NewServer(status.New(), testToken).Serve(context.Background(), "0.0.0.0:0", nil)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected a loopback-only refusal, got %v", err)
	}
}

func TestServe_BindsLoopbackAndServes(t *testing.T) {
	m := status.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addrCh := make(chan string, 1)
	go func() {
		_ = NewServer(m, testToken).Serve(ctx, "127.0.0.1:0", func(a net.Addr) {
			addrCh <- a.String()
		})
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(3 * time.Second):
		t.Fatal("server never bound")
	}

	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dcancel()
	conn, _, err := websocket.Dial(dctx, "ws://"+addr, nil)
	if err != nil {
		t.Fatalf("dial bound server: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := wsjson.Write(dctx, conn, authMsg{Token: testToken}); err != nil {
		t.Fatal(err)
	}
	var msg statusMsg
	if err := wsjson.Read(dctx, conn, &msg); err != nil {
		t.Fatalf("read from bound server: %v", err)
	}
}

// anyMsg reads any server->client frame so a test can dispatch on type.
type anyMsg struct {
	Type     string          `json:"type"`
	Snapshot status.Snapshot `json:"snapshot"`
	Beacon   []byte          `json:"beacon"`
}

func readAny(t *testing.T, ctx context.Context, conn *websocket.Conn) anyMsg {
	t.Helper()
	var msg anyMsg
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	return msg
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestServer_PublishPushesBeaconToClient(t *testing.T) {
	s := NewServer(status.New(), testToken)
	s.SetBeacon([]byte("sealed-beacon-one")) // set before any client connects
	srv := httptest.NewServer(s)
	defer srv.Close()

	conn, ctx := dialAuthed(t, srv, testToken)
	defer func() { _ = conn.CloseNow() }()

	// On connect the client gets the current status and the pending beacon.
	got := map[string]anyMsg{}
	for i := 0; i < 2; i++ {
		m := readAny(t, ctx, conn)
		got[m.Type] = m
	}
	if _, ok := got["status"]; !ok {
		t.Fatal("client never received initial status")
	}
	if string(got["beacon"].Beacon) != "sealed-beacon-one" {
		t.Fatalf("beacon not delivered on connect: %q", got["beacon"].Beacon)
	}

	// A new beacon is pushed live.
	s.SetBeacon([]byte("sealed-beacon-two"))
	m := readAny(t, ctx, conn)
	if m.Type != "beacon" || string(m.Beacon) != "sealed-beacon-two" {
		t.Fatalf("updated beacon not pushed: %+v", m)
	}
}

func TestServer_ReportedPeersAreFetchable(t *testing.T) {
	s := NewServer(status.New(), testToken)
	srv := httptest.NewServer(s)
	defer srv.Close()

	conn, ctx := dialAuthed(t, srv, testToken)
	defer func() { _ = conn.CloseNow() }()
	readAny(t, ctx, conn) // drain the initial status

	// The extension reports the peer beacons it sees in storage.sync.
	want := [][]byte{[]byte("peer-A-sealed"), []byte("peer-B-sealed")}
	if err := wsjson.Write(ctx, conn, clientMsg{Type: "peers", Beacons: want}); err != nil {
		t.Fatal(err)
	}

	// Fetch (the Signaling method) returns them once the read pump has processed.
	eventually(t, func() bool { return len(s.PeerBeacons()) == 2 })
	got, err := s.Fetch(context.Background(), "self")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != "peer-A-sealed" || string(got[1]) != "peer-B-sealed" {
		t.Fatalf("Fetch returned the wrong peers: %q", got)
	}
}

func TestServer_SignalingMethods(t *testing.T) {
	s := NewServer(status.New(), testToken)
	if err := s.Publish(context.Background(), "self", []byte("my-beacon")); err != nil {
		t.Fatal(err)
	}
	if string(s.currentBeacon()) != "my-beacon" {
		t.Fatalf("Publish did not store the beacon: %q", s.currentBeacon())
	}
	s.setPeers([][]byte{[]byte("p1")})
	got, _ := s.Fetch(context.Background(), "self")
	if len(got) != 1 || string(got[0]) != "p1" {
		t.Fatalf("Fetch did not reflect reported peers: %q", got)
	}
}

func TestToken_DeterministicAndKeyScoped(t *testing.T) {
	k1, err := crypto.DeriveKeys("correct horse battery staple lorem", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := crypto.DeriveKeys("correct horse battery staple lorem", crypto.AppSalt)
	k3, _ := crypto.DeriveKeys("a different passphrase entirely here", crypto.AppSalt)

	t1, err := Token(k1)
	if err != nil {
		t.Fatal(err)
	}
	t2, _ := Token(k2)
	t3, _ := Token(k3)
	if t1 != t2 {
		t.Fatal("token is not deterministic for the same passphrase")
	}
	if t1 == t3 {
		t.Fatal("different passphrases produced the same token")
	}
	if len(t1) != 64 { // 32 bytes hex
		t.Fatalf("token length = %d, want 64 hex chars", len(t1))
	}
}
