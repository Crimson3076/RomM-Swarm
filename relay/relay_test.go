package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sort"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func testBridges(t *testing.T) (protocol.BridgeID, protocol.BridgeID) {
	t.Helper()
	return protocol.BridgeIDFromPublicKey([]byte("source-key")),
		protocol.BridgeIDFromPublicKey([]byte("sink-key"))
}

// connectPipe wires a client-side net.Conn (returned to the test) to a
// server-side net.Conn handled by s.handle in its own goroutine — the same
// per-connection handling Serve would give a real Accept()ed connection, but
// without needing a real listener or port.
func connectPipe(s *Server) net.Conn {
	client, server := net.Pipe()
	go s.handle(server)
	return client
}

// connectResult is what connectAsync delivers once Connect returns.
type connectResult struct {
	r   interface{ Read([]byte) (int, error) }
	ack AckFrame
	err error
}

// connectAsync runs Connect in its own goroutine and returns a channel for
// the result. Connect blocks until its connection is paired (or definitely
// fails to pair), so a source and a sink must always be connected
// concurrently, never one synchronously before the other — a test that calls
// Connect twice in sequence on the same goroutine deadlocks, because the
// first call never returns until the second one has even been attempted.
func connectAsync(conn net.Conn, frame ConnectFrame) <-chan connectResult {
	ch := make(chan connectResult, 1)
	go func() {
		r, ack, err := Connect(conn, frame)
		ch <- connectResult{r, ack, err}
	}()
	return ch
}

func mustConnect(t *testing.T, label string, ch <-chan connectResult) connectResult {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("%s Connect: %v", label, res.err)
		}
		return res
	case <-time.After(5 * time.Second):
		t.Fatalf("%s Connect never returned", label)
		return connectResult{}
	}
}

// TestPhase0_RelayPairsSourceAndSinkAndRelaysBytes is ADR 0002's evaluation
// criterion 2 at the relay's own layer: an encrypted relayed path is
// established, and bytes actually move end to end.
func TestPhase0_RelayPairsSourceAndSinkAndRelaysBytes(t *testing.T) {
	source, sink := testBridges(t)
	grant := protocol.NewGrantID()

	reportedCh := make(chan int64, 1)
	s := &Server{OnRelayed: func(g protocol.GrantID, thisSession, total int64) {
		if g != grant {
			t.Errorf("OnRelayed reported grant %s, want %s", g, grant)
		}
		reportedCh <- thisSession
	}}

	sourceConn := connectPipe(s)
	sinkConn := connectPipe(s)

	sourceCh := connectAsync(sourceConn, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	sinkCh := connectAsync(sinkConn, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})

	sourceRes := mustConnect(t, "source", sourceCh)
	sinkRes := mustConnect(t, "sink", sinkCh)
	if sourceRes.ack.Offset != 0 || sinkRes.ack.Offset != 0 {
		t.Fatalf("fresh grant resumed at source=%d sink=%d, want 0/0", sourceRes.ack.Offset, sinkRes.ack.Offset)
	}

	payload := []byte("this is the payload that moves through the relay")
	done := make(chan error, 1)
	go func() {
		_, err := sourceConn.Write(payload)
		sourceConn.Close() // signals EOF to the relay's io.Copy
		done <- err
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(sinkRes.r, got); err != nil {
		t.Fatalf("reading relayed bytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("relayed payload = %q, want %q", got, payload)
	}
	if err := <-done; err != nil {
		t.Fatalf("writing the payload: %v", err)
	}

	select {
	case got := <-reportedCh:
		if got != int64(len(payload)) {
			t.Fatalf("OnRelayed reported %d bytes, want %d", got, len(payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnRelayed was never called")
	}
}

// TestPhase0_RelayResumesFromItsOwnRecordedOffsetNotTheClients is ADR 0002's
// evaluation criteria 3 and 4 at the relay's own layer: a resumed session
// picks up from where the relay itself left off, and a client cannot shorten
// its own history to evade the byte budget by understating what it already
// sent.
func TestPhase0_RelayResumesFromItsOwnRecordedOffsetNotTheClients(t *testing.T) {
	source, sink := testBridges(t)
	grant := protocol.NewGrantID()
	s := &Server{}

	// First session: relay 10 bytes, then the source disconnects (an
	// interruption, not a clean handoff).
	sc1 := connectPipe(s)
	kc1 := connectPipe(s)
	sc1Ch := connectAsync(sc1, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	kc1Ch := connectAsync(kc1, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})
	mustConnect(t, "session 1 source", sc1Ch)
	kc1Res := mustConnect(t, "session 1 sink", kc1Ch)

	first := []byte("0123456789")
	sc1.Write(first)
	buf := make([]byte, len(first))
	io.ReadFull(kc1Res.r, buf)
	sc1.Close() // interrupted
	kc1.Close()

	time.Sleep(20 * time.Millisecond) // let the relay's accounting settle

	// Second session, same grant: the client lies and claims it has nothing
	// yet (ResumeOffset: 0). The relay must still report its own count. No
	// sink reconnects in this session — a source alone is enough to observe
	// what offset the relay hands back.
	sc2 := connectPipe(s)
	defer sc2.Close()
	sc2Ch := connectAsync(sc2, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink, ResumeOffset: 0})
	kc2 := connectPipe(s)
	defer kc2.Close()
	kc2Ch := connectAsync(kc2, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})

	sc2Res := mustConnect(t, "session 2 source", sc2Ch)
	mustConnect(t, "session 2 sink", kc2Ch)
	if sc2Res.ack.Offset != int64(len(first)) {
		t.Fatalf("resumed at %d, want the relay's own recorded %d regardless of the client's claim", sc2Res.ack.Offset, len(first))
	}
}

// TestPhase0_RelayEnforcesTheConfiguredByteLimit is ADR 0012: "Byte ...
// limits, configured independently of the Host's limits."
func TestPhase0_RelayEnforcesTheConfiguredByteLimit(t *testing.T) {
	source, sink := testBridges(t)
	grant := protocol.NewGrantID()
	s := &Server{Config: Config{MaxBytesPerGrant: 5}}

	sc := connectPipe(s)
	kc := connectPipe(s)
	scCh := connectAsync(sc, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	kcCh := connectAsync(kc, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})
	mustConnect(t, "source", scCh)
	kcRes := mustConnect(t, "sink", kcCh)

	go func() {
		sc.Write([]byte("far more than the five-byte budget allows"))
		sc.Close()
	}()

	got, err := io.ReadAll(kcRes.r)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reading relayed bytes: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("relayed %d bytes past a 5-byte MaxBytesPerGrant, want exactly 5", len(got))
	}
}

// TestPhase0_RelayEnforcesTheConfiguredConcurrencyLimit is ADR 0012:
// "concurrency ... limits, configured independently of the Host's limits."
func TestPhase0_RelayEnforcesTheConfiguredConcurrencyLimit(t *testing.T) {
	sourceA, sinkA := testBridges(t)
	s := &Server{Config: Config{MaxConcurrent: 1}}
	grantA := protocol.NewGrantID()
	grantB := protocol.NewGrantID()

	// Pair grant A and leave it open (no bytes written, no close) so it
	// continues occupying the single concurrency slot.
	scA := connectPipe(s)
	kcA := connectPipe(s)
	defer scA.Close()
	defer kcA.Close()
	scACh := connectAsync(scA, ConnectFrame{Grant: grantA, Role: RoleSource, Source: sourceA, Sink: sinkA})
	kcACh := connectAsync(kcA, ConnectFrame{Grant: grantA, Role: RoleSink, Source: sourceA, Sink: sinkA})
	mustConnect(t, "grant A source", scACh)
	mustConnect(t, "grant A sink", kcACh)

	time.Sleep(20 * time.Millisecond) // let A's pairing actually admit and start piping

	// Grant B tries to pair while A is still occupying the only slot.
	sourceB, sinkB := protocol.BridgeIDFromPublicKey([]byte("b-source")), protocol.BridgeIDFromPublicKey([]byte("b-sink"))
	scB := connectPipe(s)
	kcB := connectPipe(s)
	defer scB.Close()
	defer kcB.Close()
	scBCh := connectAsync(scB, ConnectFrame{Grant: grantB, Role: RoleSource, Source: sourceB, Sink: sinkB})
	kcBCh := connectAsync(kcB, ConnectFrame{Grant: grantB, Role: RoleSink, Source: sourceB, Sink: sinkB})

	var sawRejection bool
	for _, res := range []connectResult{<-scBCh, <-kcBCh} {
		if res.err != nil {
			sawRejection = true
			if res.ack.Err == "" {
				t.Fatal("a concurrency rejection carried no explanation")
			}
		}
	}
	if !sawRejection {
		t.Fatal("grant B paired despite the concurrency limit")
	}
}

// TestPhase0_RevokeTearsDownAnActiveSessionAndRejectsFutureOnes is ADR 0012:
// "revocable grants."
func TestPhase0_RevokeTearsDownAnActiveSessionAndRejectsFutureOnes(t *testing.T) {
	source, sink := testBridges(t)
	grant := protocol.NewGrantID()
	s := &Server{}

	sc := connectPipe(s)
	kc := connectPipe(s)
	scCh := connectAsync(sc, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	kcCh := connectAsync(kc, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})
	mustConnect(t, "source", scCh)
	kcRes := mustConnect(t, "sink", kcCh)

	s.Revoke(grant)

	// The already-active pairing must be torn down immediately, not merely
	// left to expire on its own: the sink's read unblocks right away, well
	// before the generous deadline below, which exists only to fail the test
	// promptly if revocation did nothing.
	kc.SetReadDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()
	buf := make([]byte, 1)
	_, readErr := kcRes.r.Read(buf)
	if readErr == nil {
		t.Error("the sink kept reading after its grant was revoked")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("the sink's read only unblocked after %s, which is the read deadline firing, not real revocation", elapsed)
	}

	// A brand-new connection for the same grant is rejected immediately.
	sc2 := connectPipe(s)
	defer sc2.Close()
	_, ack, err := Connect(sc2, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	if err == nil {
		t.Fatalf("a revoked grant accepted a new connection; ack: %+v", ack)
	}
}

// TestPairingTimesOutWhenNoPeerArrives proves a lone connection is not held
// forever when its counterpart never shows up.
func TestPairingTimesOutWhenNoPeerArrives(t *testing.T) {
	source, sink := testBridges(t)
	s := &Server{Config: Config{PairingTimeout: 50 * time.Millisecond}}

	sc := connectPipe(s)
	defer sc.Close()
	_, ack, err := Connect(sc, ConnectFrame{Grant: protocol.NewGrantID(), Role: RoleSource, Source: source, Sink: sink})
	if err == nil {
		t.Fatalf("a lone connection was not rejected after the pairing timeout; ack: %+v", ack)
	}
}

// TestRelayRejectsAMalformedFrame proves a garbage first line closes the
// connection rather than panicking or hanging the server.
func TestRelayRejectsAMalformedFrame(t *testing.T) {
	s := &Server{}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()

	go func() {
		client.SetWriteDeadline(time.Now().Add(2 * time.Second))
		client.Write([]byte("not json at all\n"))
	}()

	// Drain the server's error Ack so its write doesn't block forever on this
	// unbuffered pipe — a real client would read this too.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	io.ReadAll(client)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the server did not close the connection after a malformed frame")
	}
}

// TestConnectFrameCarriesNoCredentialField locks the wire protocol's field
// list to exactly what the package doc claims. A future field added here
// without updating this test is a signal worth stopping on, not passing
// silently — see the package doc comment on ConnectFrame.
func TestConnectFrameCarriesNoCredentialField(t *testing.T) {
	b, err := json.Marshal(ConnectFrame{
		Grant: protocol.NewGrantID(), Role: RoleSource,
		Source: protocol.BridgeIDFromPublicKey([]byte("a")),
		Sink:   protocol.BridgeIDFromPublicKey([]byte("b")),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	want := []string{"grant", "resume_offset", "role", "sink", "source"}
	if len(keys) != len(want) {
		t.Fatalf("ConnectFrame carries fields %v, want exactly %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("ConnectFrame carries fields %v, want exactly %v", keys, want)
		}
	}
}

// TestPhase0_RelayServesOverARealListener proves Serve itself works end to
// end, not just the per-connection handling the other tests exercise via
// connectPipe.
func TestPhase0_RelayServesOverARealListener(t *testing.T) {
	source, sink := testBridges(t)
	grant := protocol.NewGrantID()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	s := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, ln)

	sourceConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer sourceConn.Close()
	sinkConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer sinkConn.Close()

	sourceCh := connectAsync(sourceConn, ConnectFrame{Grant: grant, Role: RoleSource, Source: source, Sink: sink})
	sinkCh := connectAsync(sinkConn, ConnectFrame{Grant: grant, Role: RoleSink, Source: source, Sink: sink})
	mustConnect(t, "source", sourceCh)
	sinkRes := mustConnect(t, "sink", sinkCh)

	payload := []byte("over a real TCP listener this time")
	go func() {
		sourceConn.Write(payload)
		sourceConn.(*net.TCPConn).CloseWrite()
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(sinkRes.r, got); err != nil {
		t.Fatalf("reading relayed bytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("relayed payload = %q, want %q", got, payload)
	}
}
