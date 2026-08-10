// Package relay implements the transfer relay: the encrypted fallback path a
// Bridge uses when it cannot reach a peer directly, per Scope of Work §3 and
// §5.6, and ADR 0012's deployment-separation decision.
//
// The relay authenticates a short-lived transfer grant and nothing else. It
// never receives a RomM credential and never receives a Host database
// credential — ConnectFrame, the entire protocol surface this package
// understands, has no field for either, which makes that a fact about the
// wire protocol rather than a promise about code elsewhere remembering not to
// send one. It holds no durable storage: bytes are copied source-to-sink
// in-flight and never written to disk, per ADR 0012 and Scope of Work §5.6.
//
// TLS termination is a deployment concern, not this package's: Serve accepts
// any net.Listener, and a caller wanting encryption wraps one with
// tls.NewListener before calling Serve. Keeping the two separate is what
// makes the pairing and limits logic below testable over a plain loopback
// listener.
package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Role is which side of a relayed transfer a connection plays.
type Role string

const (
	RoleSource Role = "source" // holds the content, sends bytes
	RoleSink   Role = "sink"   // receives the content
)

// ConnectFrame is the first and only thing a client sends before either
// waiting to be paired or transfer bytes begin flowing. See the package doc
// for why its field list is deliberately exhaustive of what the relay is
// allowed to know.
type ConnectFrame struct {
	Grant  protocol.GrantID  `json:"grant"`
	Role   Role              `json:"role"`
	Source protocol.BridgeID `json:"source"`
	Sink   protocol.BridgeID `json:"sink"`

	// ResumeOffset is how many bytes of this grant's transfer the client
	// believes have already moved, when reconnecting after an interruption or
	// a route switch. Zero for a fresh session. Advisory only: the relay's own
	// count in Ack.Offset is authoritative, so a client cannot shorten its own
	// history to evade MaxBytesPerGrant by understating it.
	ResumeOffset int64 `json:"resume_offset"`
}

// AckFrame is the relay's reply to a ConnectFrame, sent to both sides only
// once they are paired (or once pairing has definitely failed), before any
// transfer bytes flow.
type AckFrame struct {
	// Offset is the byte position the relay has actually recorded for this
	// grant. Both sides realign to this value rather than trusting their own
	// bookkeeping.
	Offset int64 `json:"offset"`
	// Err is non-empty when pairing failed; the connection is closed
	// immediately after this frame either way.
	Err string `json:"err,omitempty"`
}

// Config bounds one relay server. ADR 0012: "Byte, time, concurrency, and
// bandwidth limits, configured independently of the Host's limits."
type Config struct {
	// MaxBytesPerGrant caps total bytes relayed for one grant across every
	// reconnect. Zero means DefaultMaxBytesPerGrant.
	MaxBytesPerGrant int64
	// MaxDuration caps how long a grant may be relayed for, measured from the
	// first connection seen for it. Zero means DefaultMaxDuration.
	MaxDuration time.Duration
	// MaxConcurrent caps how many grants may be actively relaying at once,
	// server-wide. Zero means DefaultMaxConcurrent.
	MaxConcurrent int
	// MaxBytesPerSecond caps relay throughput per active session. Zero means
	// unlimited.
	MaxBytesPerSecond int64
	// PairingTimeout bounds how long a connection waits for its counterpart
	// before the relay gives up on it. Zero means DefaultPairingTimeout.
	PairingTimeout time.Duration
}

// Defaults. MaxBytesPerGrant is generous over the largest initial-platform
// file (a Nintendo DS cartridge, up to 512 MiB) with headroom, on the same
// reasoning as bridge/scan.DefaultMaxDownloadBytes.
const (
	DefaultMaxBytesPerGrant = 4 << 30
	DefaultMaxDuration      = 2 * time.Hour
	DefaultMaxConcurrent    = 64
	DefaultPairingTimeout   = 30 * time.Second
)

func (c Config) maxBytes() int64 {
	if c.MaxBytesPerGrant > 0 {
		return c.MaxBytesPerGrant
	}
	return DefaultMaxBytesPerGrant
}

func (c Config) maxDuration() time.Duration {
	if c.MaxDuration > 0 {
		return c.MaxDuration
	}
	return DefaultMaxDuration
}

func (c Config) maxConcurrent() int {
	if c.MaxConcurrent > 0 {
		return c.MaxConcurrent
	}
	return DefaultMaxConcurrent
}

func (c Config) pairingTimeout() time.Duration {
	if c.PairingTimeout > 0 {
		return c.PairingTimeout
	}
	return DefaultPairingTimeout
}

// grantState is the relay's entire memory of one grant: how many bytes have
// moved, when it was first seen, and whether it has been revoked. Nothing
// else — no title, no filename, no credential.
type grantState struct {
	mu           sync.Mutex
	firstSeen    time.Time
	bytesRelayed int64
	revoked      bool

	waitingConn net.Conn
	waitingRole Role
	matched     chan struct{}

	// activeSrc and activeSink are set while a pairing is actively relaying
	// bytes (between pairAndRelay handing off to pipe and pipe returning), so
	// Revoke can tear down a session that is already in flight, not only one
	// still waiting to be paired.
	activeSrc, activeSink net.Conn
}

// Server is the transfer relay.
type Server struct {
	Config Config

	// Now is injectable for tests.
	Now func() time.Time

	// OnRelayed, if set, is called after each pairing ends: which grant, and
	// how many bytes moved this session. Never a title, never a filename —
	// ADR 0012: "Relay usage receipts record that a transfer was relayed and
	// how many bytes moved. They do not retain exact title names long term."
	OnRelayed func(grant protocol.GrantID, bytesThisSession, bytesTotal int64)

	mu     sync.Mutex
	grants map[protocol.GrantID]*grantState
	active int
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) state(grant protocol.GrantID) *grantState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = map[protocol.GrantID]*grantState{}
	}
	st, ok := s.grants[grant]
	if !ok {
		st = &grantState{firstSeen: s.now()}
		s.grants[grant] = st
	}
	return st
}

// Revoke ends a grant immediately: an already-active relayed session is torn
// down mid-flight, a connection still waiting for its counterpart is
// rejected, and no future connection for this grant will be paired.
func (s *Server) Revoke(grant protocol.GrantID) {
	st := s.state(grant)
	st.mu.Lock()
	st.revoked = true
	waiting := st.waitingConn
	waitingCh := st.matched
	st.waitingConn, st.matched = nil, nil
	src, sink := st.activeSrc, st.activeSink
	st.mu.Unlock()

	if waiting != nil {
		sendAckAndClose(waiting, AckFrame{Err: "relay: this grant has been revoked"})
	}
	if waitingCh != nil {
		select {
		case waitingCh <- struct{}{}:
		default:
		}
	}
	// Closing an in-flight session's connections unblocks pipe's io.Copy
	// immediately, from whichever side is currently blocked in Read or Write.
	if src != nil {
		src.Close()
	}
	if sink != nil {
		sink.Close()
	}
}

func (s *Server) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.Config.maxConcurrent() {
		return false
	}
	s.active++
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 {
		s.active--
	}
}

// Serve accepts connections from ln until ctx is cancelled or ln errors.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	conn.SetReadDeadline(s.now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	var frame ConnectFrame
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		sendAckAndClose(conn, AckFrame{Err: "relay: malformed connect frame"})
		return
	}
	if err := validateFrame(frame); err != nil {
		sendAckAndClose(conn, AckFrame{Err: err.Error()})
		return
	}

	st := s.state(frame.Grant)
	st.mu.Lock()
	if st.revoked {
		st.mu.Unlock()
		sendAckAndClose(conn, AckFrame{Err: "relay: this grant has been revoked"})
		return
	}
	resumeAt := st.bytesRelayed

	if st.waitingConn != nil && st.waitingRole != frame.Role {
		peer, peerCh := st.waitingConn, st.matched
		st.waitingConn, st.matched = nil, nil
		st.mu.Unlock()
		s.pairAndRelay(frame, st, conn, peer, peerCh, resumeAt)
		return
	}

	// No match yet: become the waiting side. The matching goroutine, once it
	// arrives, owns this connection entirely from that point on — this
	// goroutine touches it again only if pairing never happens.
	myCh := make(chan struct{}, 1)
	st.waitingConn = conn
	st.waitingRole = frame.Role
	st.matched = myCh
	st.mu.Unlock()

	select {
	case <-myCh:
		return
	case <-time.After(s.Config.pairingTimeout()):
		st.mu.Lock()
		stillWaiting := st.waitingConn == conn
		if stillWaiting {
			st.waitingConn, st.matched = nil, nil
		}
		st.mu.Unlock()
		if stillWaiting {
			sendAckAndClose(conn, AckFrame{Err: "relay: no peer connected for this grant before the pairing timeout"})
		}
	}
}

// pairAndRelay is called by whichever connection completes a pairing. It owns
// both connections from here on; the other goroutine (blocked on peerCh) does
// nothing further to its own conn once signalled.
func (s *Server) pairAndRelay(frame ConnectFrame, st *grantState, conn, peer net.Conn, peerCh chan struct{}, resumeAt int64) {
	var src, sink net.Conn
	if frame.Role == RoleSource {
		src, sink = conn, peer
	} else {
		src, sink = peer, conn
	}

	if !s.admit() {
		err := "relay: too many concurrent sessions"
		sendAckAndClose(src, AckFrame{Err: err})
		sendAckAndClose(sink, AckFrame{Err: err})
		peerCh <- struct{}{}
		return
	}

	if err := sendAck(src, AckFrame{Offset: resumeAt}); err != nil {
		src.Close()
		sink.Close()
		s.release()
		peerCh <- struct{}{}
		return
	}
	if err := sendAck(sink, AckFrame{Offset: resumeAt}); err != nil {
		src.Close()
		sink.Close()
		s.release()
		peerCh <- struct{}{}
		return
	}

	peerCh <- struct{}{}
	s.pipe(frame.Grant, st, src, sink)
}

// pipe copies bytes from src to sink under this grant's remaining byte and
// time budget, then records how much moved. It is the only place in this
// package that touches transfer bytes, and it never writes them anywhere but
// straight through to sink.
func (s *Server) pipe(grant protocol.GrantID, st *grantState, src, sink net.Conn) {
	defer s.release()
	defer src.Close()
	defer sink.Close()

	st.mu.Lock()
	if st.revoked {
		st.mu.Unlock()
		return
	}
	deadline := st.firstSeen.Add(s.Config.maxDuration())
	remaining := s.Config.maxBytes() - st.bytesRelayed
	st.activeSrc, st.activeSink = src, sink
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.activeSrc, st.activeSink = nil, nil
		st.mu.Unlock()
	}()

	if remaining <= 0 {
		return
	}
	if until := time.Until(deadline); until > 0 {
		src.SetDeadline(deadline)
		sink.SetDeadline(deadline)
	} else {
		return
	}

	limited := &rateLimitedReader{r: io.LimitReader(src, remaining), bytesPerSec: s.Config.MaxBytesPerSecond}
	n, _ := io.Copy(sink, limited)

	st.mu.Lock()
	st.bytesRelayed += n
	total := st.bytesRelayed
	st.mu.Unlock()

	if s.OnRelayed != nil {
		s.OnRelayed(grant, n, total)
	}
}

func validateFrame(f ConnectFrame) error {
	if err := f.Grant.Validate(); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	if f.Role != RoleSource && f.Role != RoleSink {
		return fmt.Errorf("relay: unknown role %q", f.Role)
	}
	if err := f.Source.Validate(); err != nil {
		return fmt.Errorf("relay: source: %w", err)
	}
	if err := f.Sink.Validate(); err != nil {
		return fmt.Errorf("relay: sink: %w", err)
	}
	if f.Source == f.Sink {
		return errors.New("relay: source and sink must be different Bridges")
	}
	if f.ResumeOffset < 0 {
		return errors.New("relay: negative resume offset")
	}
	return nil
}

func sendAck(conn net.Conn, ack AckFrame) error {
	b, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = conn.Write(b)
	conn.SetWriteDeadline(time.Time{})
	return err
}

func sendAckAndClose(conn net.Conn, ack AckFrame) {
	_ = sendAck(conn, ack)
	conn.Close()
}

// rateLimitedReader caps throughput to bytesPerSec by sleeping proportionally
// to what each Read returned. Not a production token bucket — no burst
// allowance — but enough to prove and test that a configured cap is actually
// enforced rather than merely configured.
type rateLimitedReader struct {
	r           io.Reader
	bytesPerSec int64
}

func (rl *rateLimitedReader) Read(p []byte) (int, error) {
	if rl.bytesPerSec <= 0 {
		return rl.r.Read(p)
	}
	if int64(len(p)) > rl.bytesPerSec {
		p = p[:rl.bytesPerSec]
	}
	n, err := rl.r.Read(p)
	if n > 0 {
		time.Sleep(time.Duration(float64(n) / float64(rl.bytesPerSec) * float64(time.Second)))
	}
	return n, err
}

// Connect performs the client half of the handshake on an already-established
// connection: sends frame, then reads the relay's Ack.
//
// It returns the *bufio.Reader it used, and callers must keep reading from
// that reader rather than from conn directly — the relay may have written
// transfer bytes immediately after the Ack line, which can already be sitting
// in the reader's buffer.
func Connect(conn net.Conn, frame ConnectFrame) (*bufio.Reader, AckFrame, error) {
	b, err := json.Marshal(frame)
	if err != nil {
		return nil, AckFrame{}, err
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		return nil, AckFrame{}, err
	}

	r := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := r.ReadString('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, AckFrame{}, err
	}
	var ack AckFrame
	if err := json.Unmarshal([]byte(line), &ack); err != nil {
		return nil, AckFrame{}, err
	}
	if ack.Err != "" {
		return r, ack, errors.New(ack.Err)
	}
	return r, ack, nil
}
