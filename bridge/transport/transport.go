// Package transport implements ADR 0002's connectivity decision: a direct
// Bridge-to-Bridge connection when one is reachable, falling back to the
// encrypted relay when it is not, with a transfer able to resume and switch
// route mid-flight without restarting from zero or losing its grant binding.
//
// What this package proves, and what it does not:
//
//   - It proves the routing and resume *decision logic* — direct-first,
//     relay-fallback, and a mid-transfer route switch that neither loses nor
//     duplicates bytes — against real network connections (loopback TCP in
//     tests, the same code path production would use).
//   - It does not prove connectivity under a real carrier-grade NAT. That
//     needs two real hosts, one genuinely behind CGNAT — see ADR 0002's
//     Consequences and docs/phase0/acceptance-evidence.md, B2. A simulated
//     "direct dial fails" here (an address nothing listens on) exercises the
//     same fallback code path a real CGNAT rejection would, but is not a
//     substitute for that hardware proof.
//   - It does not choose or implement direct-path encryption. Which key
//     material would authenticate a direct Bridge-to-Bridge connection is an
//     open design question this codebase has not settled (see ADR 0002's
//     open questions) and is not decided here — DirectDialer is deliberately
//     plain TCP. The relay path IS meaningfully encrypted in production,
//     using ordinary server-authenticated TLS on the relay's listener
//     (relay.Server.Serve accepts any net.Listener, so a caller wraps one
//     with tls.NewListener) — that requires no new Bridge identity design,
//     the same way any HTTPS server's certificate does not.
package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/relay"
)

// Route names which path a connection actually used.
type Route string

const (
	RouteDirect Route = "direct"
	RouteRelay  Route = "relay"
)

// Endpoint identifies the transfer a connection carries: the grant
// authorizing it and the two Bridge identities it is bound to. The same
// Endpoint is used for every dial attempt across a transfer, on every route —
// that sameness is what "remains bound to its grant and Bridge identities"
// (ADR 0002 criterion 4) means concretely: the identifiers travel with the
// transfer across a route switch rather than being renegotiated.
type Endpoint struct {
	Grant  protocol.GrantID
	Source protocol.BridgeID
	Sink   protocol.BridgeID
}

// Peer is where to reach the other side. At least one address must be set.
type Peer struct {
	// DirectAddr is the peer's advertised direct address (host:port), empty
	// if none is known or reachable.
	DirectAddr string
	// RelayAddr is a relay server's address (host:port), empty if none is
	// configured for this Swarm.
	RelayAddr string
}

// Dialer establishes one direct connection to an address. DirectDialer is the
// production implementation; tests may supply another to inject failure
// modes a real network only produces occasionally (a connection that dies
// partway through, for instance).
type Dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

// DirectDialer connects straight to a peer's advertised address. See the
// package doc for why this is plain TCP rather than an encrypted transport.
type DirectDialer struct {
	// Timeout bounds one dial attempt. Zero means DefaultDirectTimeout.
	//
	// Deliberately short: a direct dial to a peer behind CGNAT is expected to
	// fail (connection refused or a black hole) rather than eventually
	// succeed, so Router should find that out quickly and fall back to the
	// relay within a reasonable overall connect time, not wait out a long
	// default TCP timeout first.
	Timeout time.Duration
}

// DefaultDirectTimeout is short on purpose — see DirectDialer.Timeout.
const DefaultDirectTimeout = 5 * time.Second

func (d DirectDialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return DefaultDirectTimeout
}

// Dial attempts one direct TCP connection.
func (d DirectDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: d.timeout()}
	return dialer.DialContext(ctx, "tcp", addr)
}

// Router establishes one connection to a peer for one role, direct-first with
// an encrypted-relay fallback — ADR 0002 criteria 1 and 2.
type Router struct {
	// Direct dials the direct path. Nil means DirectDialer{}.
	Direct Dialer

	// RelayDialTimeout bounds connecting to the relay server itself,
	// distinct from DirectDialer.Timeout. Zero means DefaultRelayDialTimeout.
	RelayDialTimeout time.Duration
}

func (r Router) direct() Dialer {
	if r.Direct != nil {
		return r.Direct
	}
	return DirectDialer{}
}

// DefaultRelayDialTimeout is generous relative to DirectDialer's default: the
// relay is expected to be reachable, so there is no reason to fail fast here
// the way there is for a possibly-CGNAT'd direct peer.
const DefaultRelayDialTimeout = 10 * time.Second

func (r Router) relayDialTimeout() time.Duration {
	if r.RelayDialTimeout > 0 {
		return r.RelayDialTimeout
	}
	return DefaultRelayDialTimeout
}

// Dial is the source-side entry point: try direct, then fall back to relay.
// resumeOffset is advisory for the relay route (the relay's own recorded
// offset, returned as actualOffset, is authoritative — see relay.ConnectFrame)
// and is echoed back unchanged for the direct route, which has no third party
// to arbitrate it.
func (r Router) Dial(ctx context.Context, peer Peer, ep Endpoint, role relay.Role, resumeOffset int64) (conn io.ReadWriteCloser, route Route, actualOffset int64, err error) {
	if peer.DirectAddr != "" {
		if c, dialErr := r.direct().Dial(ctx, peer.DirectAddr); dialErr == nil {
			return c, RouteDirect, resumeOffset, nil
		}
	}
	c, offset, err := r.dialRelay(ctx, peer, ep, role, resumeOffset)
	if err != nil {
		return nil, "", 0, err
	}
	return c, RouteRelay, offset, nil
}

// dialRelay connects to the relay and performs its handshake. Used both by
// Dial (the source side's fallback) and directly by Session's receive path,
// which has no "direct dial" of its own to attempt first — a receiver
// accepts a direct connection rather than dialing one.
func (r Router) dialRelay(ctx context.Context, peer Peer, ep Endpoint, role relay.Role, resumeOffset int64) (io.ReadWriteCloser, int64, error) {
	if peer.RelayAddr == "" {
		return nil, 0, errors.New("transport: direct route unavailable and no relay is configured")
	}
	dialer := net.Dialer{Timeout: r.relayDialTimeout()}
	conn, err := dialer.DialContext(ctx, "tcp", peer.RelayAddr)
	if err != nil {
		return nil, 0, fmt.Errorf("transport: relay unreachable: %w", err)
	}
	br, ack, err := relay.Connect(conn, relay.ConnectFrame{
		Grant: ep.Grant, Role: role, Source: ep.Source, Sink: ep.Sink, ResumeOffset: resumeOffset,
	})
	if err != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("transport: relay handshake: %w", err)
	}
	return &bufferedConn{r: br, Conn: conn}, ack.Offset, nil
}

// bufferedConn pairs the *bufio.Reader Connect used (which may already hold
// buffered transfer bytes read past the Ack line) with the underlying
// net.Conn for writes and close.
type bufferedConn struct {
	r *bufio.Reader
	net.Conn
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// DefaultChunkSize bounds how much Session reads and writes per attempt, so a
// mid-transfer route failure loses at most one chunk's worth of unconfirmed
// progress rather than needing to restart the whole remaining transfer.
const DefaultChunkSize = 64 << 10

// Session moves size bytes for one Endpoint between two peers, direct-first
// with a relay fallback, and continues across a mid-transfer route failure
// from wherever it actually got to rather than from zero — ADR 0002
// criteria 3 and 4.
type Session struct {
	Router   Router
	Peer     Peer
	Endpoint Endpoint

	// ChunkSize overrides DefaultChunkSize.
	ChunkSize int64
}

func (s Session) chunkSize() int64 {
	if s.ChunkSize > 0 {
		return s.ChunkSize
	}
	return DefaultChunkSize
}

// Send streams src[0:size] to the peer. It returns the sequence of routes
// actually used, in order — a single-element slice when nothing failed over,
// more than one when a route switch happened.
func (s Session) Send(ctx context.Context, src io.ReaderAt, size int64) ([]Route, error) {
	var routes []Route
	var lastRoute Route
	var offset int64

	for offset < size {
		conn, route, actualOffset, err := s.Router.Dial(ctx, s.Peer, s.Endpoint, relay.RoleSource, offset)
		if err != nil {
			return routes, fmt.Errorf("transport: no route available at offset %d of %d: %w", offset, size, err)
		}
		// actualOffset is the relay's own authoritative count for THIS
		// route — which is correct to trust when resuming a relay session
		// that failed and reconnected (it can be ahead of a sender that lost
		// its own bookkeeping, e.g. after a restart), but is meaningless on
		// a fresh switch from a different route: a relay that has never
		// carried this grant before reports 0 regardless of how much the
		// direct path already delivered. max() gets both right: it never
		// regresses the sender's own tracked progress, but still lets the
		// relay correct it upward when the relay's memory is longer.
		offset = max(offset, actualOffset)
		if route != lastRoute {
			routes = append(routes, route)
			lastRoute = route
		}

		n, sendErr := sendFrom(conn, src, offset, size, s.chunkSize())
		offset += n
		conn.Close()
		if sendErr == nil {
			break
		}
		if ctx.Err() != nil {
			return routes, ctx.Err()
		}
		// Otherwise the connection failed mid-stream: loop around and dial
		// again from the new offset, possibly on a different route.
	}
	return routes, nil
}

func sendFrom(w io.Writer, src io.ReaderAt, offset, size, chunkSize int64) (int64, error) {
	var sent int64
	buf := make([]byte, chunkSize)
	for offset+sent < size {
		n := chunkSize
		if remaining := size - offset - sent; n > remaining {
			n = remaining
		}
		read, err := src.ReadAt(buf[:n], offset+sent)
		if read > 0 {
			written, werr := w.Write(buf[:read])
			// written, not read, is what actually reached the connection —
			// io.Writer's contract allows a short write with a non-nil error,
			// and undercounting here would silently resend bytes the peer
			// already has, overcounting would silently skip bytes it doesn't.
			sent += int64(written)
			if werr != nil {
				return sent, werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && offset+sent >= size {
				break
			}
			return sent, err
		}
	}
	return sent, nil
}

// Receive accepts size bytes into dst: at most one direct connection from ln
// (if ln is non-nil), then a relay fallback for whatever is left, continuing
// across a route failure the same way Send does. ln is optional because a
// receiver with no reachable direct address (the CGNAT case) has nothing to
// listen on and goes straight to relay.
func (s Session) Receive(ctx context.Context, ln net.Listener, dst io.Writer, size int64) ([]Route, error) {
	var routes []Route
	var lastRoute Route
	var offset int64
	directAttempted := false

	for offset < size {
		var conn io.ReadWriteCloser
		var route Route

		if ln != nil && !directAttempted {
			directAttempted = true
			if c, err := acceptOne(ctx, ln); err == nil {
				conn, route = c, RouteDirect
			}
			// An accept failure just falls through to the relay branch below
			// — a direct listener with no incoming connection (or one that
			// failed) is exactly the CGNAT-fallback case.
		}

		if conn == nil {
			c, _, err := s.Router.dialRelay(ctx, s.Peer, s.Endpoint, relay.RoleSink, offset)
			if err != nil {
				return routes, fmt.Errorf("transport: no route available at offset %d of %d: %w", offset, size, err)
			}
			conn, route = c, RouteRelay
		}

		if route != lastRoute {
			routes = append(routes, route)
			lastRoute = route
		}

		n, err := io.CopyN(dst, conn, size-offset)
		offset += n
		conn.Close()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return routes, ctx.Err()
		}
		// Otherwise the connection failed mid-stream: loop around. The next
		// iteration goes straight to relay, since directAttempted is now true.
	}
	return routes, nil
}

func acceptOne(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- result{conn, err}
	}()
	select {
	case res := <-ch:
		return res.conn, res.err
	case <-ctx.Done():
		ln.Close() // unblocks the Accept goroutine above
		<-ch
		return nil, ctx.Err()
	}
}
