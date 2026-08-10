package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/relay"
)

func testEndpoint(t *testing.T) Endpoint {
	t.Helper()
	return Endpoint{
		Grant:  protocol.NewGrantID(),
		Source: protocol.BridgeIDFromPublicKey([]byte("transport-source")),
		Sink:   protocol.BridgeIDFromPublicKey([]byte("transport-sink")),
	}
}

func startRelay(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting a test relay listener: %v", err)
	}
	s := &relay.Server{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		ln.Close()
	})
	go s.Serve(ctx, ln)
	return ln.Addr().String()
}

// TestPhase0_DirectSucceedsWhenReachable is ADR 0002 criterion 1: "A direct
// path is established when the network permits one."
func TestPhase0_DirectSucceedsWhenReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	ep := testEndpoint(t)
	peer := Peer{DirectAddr: ln.Addr().String(), RelayAddr: startRelay(t)}
	payload := bytes.Repeat([]byte("direct path bytes "), 1000)

	sendDone := make(chan error, 1)
	var sendRoutes []Route
	go func() {
		sess := Session{Peer: peer, Endpoint: ep}
		routes, err := sess.Send(context.Background(), bytes.NewReader(payload), int64(len(payload)))
		sendRoutes = routes
		sendDone <- err
	}()

	recvDone := make(chan error, 1)
	var got bytes.Buffer
	var recvRoutes []Route
	go func() {
		sess := Session{Peer: peer, Endpoint: ep}
		routes, err := sess.Receive(context.Background(), ln, &got, int64(len(payload)))
		recvRoutes = routes
		recvDone <- err
	}()

	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvDone; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("received bytes do not match what was sent")
	}
	if len(sendRoutes) != 1 || sendRoutes[0] != RouteDirect {
		t.Fatalf("send used routes %v, want exactly [direct]", sendRoutes)
	}
	if len(recvRoutes) != 1 || recvRoutes[0] != RouteDirect {
		t.Fatalf("receive used routes %v, want exactly [direct]", recvRoutes)
	}
}

// TestPhase0_FallsBackToRelayWhenDirectIsUnreachable is ADR 0002 criterion 2:
// "An encrypted relayed path is established when it does not [permit a
// direct path], with no inbound port forward on either side." The unreachable
// direct address here stands in for a CGNAT rejection — see the package doc
// for why that is not itself sufficient evidence for the real go/stop gate.
func TestPhase0_FallsBackToRelayWhenDirectIsUnreachable(t *testing.T) {
	// Nothing listens on this address: the same failure shape as a peer
	// behind CGNAT refusing an unsolicited inbound connection.
	unreachable := "127.0.0.1:1" // a real TCP connect refused almost instantly

	ep := testEndpoint(t)
	peer := Peer{DirectAddr: unreachable, RelayAddr: startRelay(t)}
	payload := bytes.Repeat([]byte("relay fallback bytes "), 1000)

	sendDone := make(chan error, 1)
	var sendRoutes []Route
	go func() {
		sess := Session{Peer: peer, Endpoint: ep}
		routes, err := sess.Send(context.Background(), bytes.NewReader(payload), int64(len(payload)))
		sendRoutes = routes
		sendDone <- err
	}()

	recvDone := make(chan error, 1)
	var got bytes.Buffer
	var recvRoutes []Route
	go func() {
		// No listener at all — this receiver has no reachable direct address
		// either, exactly the CGNAT member case.
		sess := Session{Peer: peer, Endpoint: ep}
		routes, err := sess.Receive(context.Background(), nil, &got, int64(len(payload)))
		recvRoutes = routes
		recvDone <- err
	}()

	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvDone; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("received bytes do not match what was sent")
	}
	if len(sendRoutes) != 1 || sendRoutes[0] != RouteRelay {
		t.Fatalf("send used routes %v, want exactly [relay]", sendRoutes)
	}
	if len(recvRoutes) != 1 || recvRoutes[0] != RouteRelay {
		t.Fatalf("receive used routes %v, want exactly [relay]", recvRoutes)
	}
}

// truncatingConn wraps a net.Conn and severs itself after n bytes have been
// written through it, simulating a direct connection that dies mid-transfer
// rather than one that was never reachable at all.
type truncatingConn struct {
	net.Conn
	remaining int
}

func (c *truncatingConn) Write(p []byte) (int, error) {
	if c.remaining <= 0 {
		c.Close()
		return 0, net.ErrClosed
	}
	requested := len(p)
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Write(p)
	c.remaining -= n
	if c.remaining <= 0 {
		c.Close()
	}
	// io.Writer's contract: a short write (n < requested) must return a
	// non-nil error, or callers that trust n == len(p) whenever err == nil
	// will silently miscount how much actually reached the wire.
	if err == nil && n < requested {
		err = errors.New("truncatingConn: connection severed mid-write")
	}
	return n, err
}

// severingDialer hands out exactly one direct connection, truncated after
// cutBytes bytes written to simulate a direct path that worked and then died
// mid-transfer. Every subsequent Dial call fails immediately, standing in for
// "the direct path is now gone" rather than a real dial that would otherwise
// succeed again — real intermittent connectivity behaves more ambiguously
// than that, but a clean failure is what forces Send to actually exercise
// its fallback rather than hang retrying a route that no longer works.
type severingDialer struct {
	inner    DirectDialer
	cutBytes int
	used     bool
}

func (d *severingDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	if d.used {
		return nil, errors.New("severingDialer: the direct path is gone after its first connection")
	}
	d.used = true
	conn, err := d.inner.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	return &truncatingConn{Conn: conn, remaining: d.cutBytes}, nil
}

// TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes is ADR 0002
// criterion 4: "Route switching mid-transfer does not break the transfer or
// its grant binding." A transfer starts direct, the direct connection dies
// partway through, and it finishes over the relay under the same grant —
// with the full payload arriving exactly once, in order.
func TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	ep := testEndpoint(t)
	peer := Peer{DirectAddr: ln.Addr().String(), RelayAddr: startRelay(t)}
	payload := bytes.Repeat([]byte("route switch payload "), 5000) // several chunks' worth

	cutAfter := len(payload) / 3 // sever the direct connection partway through

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sendDone := make(chan error, 1)
	var sendRoutes []Route
	go func() {
		sess := Session{
			Router:   Router{Direct: &severingDialer{inner: DirectDialer{}, cutBytes: cutAfter}},
			Peer:     peer,
			Endpoint: ep,
		}
		routes, err := sess.Send(ctx, bytes.NewReader(payload), int64(len(payload)))
		sendRoutes = routes
		sendDone <- err
	}()

	recvDone := make(chan error, 1)
	var got bytes.Buffer
	var recvRoutes []Route
	go func() {
		sess := Session{Peer: peer, Endpoint: ep}
		routes, err := sess.Receive(ctx, ln, &got, int64(len(payload)))
		recvRoutes = routes
		recvDone <- err
	}()

	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-recvDone; err != nil {
		t.Fatalf("Receive: %v", err)
	}

	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("reassembled %d bytes do not match the original %d-byte payload after a route switch — bytes were lost or duplicated", got.Len(), len(payload))
	}
	if len(sendRoutes) != 2 || sendRoutes[0] != RouteDirect || sendRoutes[1] != RouteRelay {
		t.Fatalf("send routes = %v, want [direct relay] to prove an actual switch happened, not one route throughout", sendRoutes)
	}
	if len(recvRoutes) != 2 || recvRoutes[0] != RouteDirect || recvRoutes[1] != RouteRelay {
		t.Fatalf("receive routes = %v, want [direct relay]", recvRoutes)
	}
}
