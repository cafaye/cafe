package boxd

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// The outbound-only channel: the dial shape and the wire protocol.
//
// The dial is tls.Dial — a client connection the box makes to the control
// plane's address. There is no Listen, no net.Listener and no inbound handler
// in this package, and that is the physical shape of "outbound-only": nothing
// on this box accepts a connection from anywhere, so nothing outside can knock
// on the box even if it wanted to. The house rule in AGENTS.md about listeners
// is the same rule at fleet scale: `cafe mcp`'s HTTP transport is loopback-only
// because it starts containers, and this channel is outbound-only because it
// redeems capabilities.
//
// The wire is one JSON object per line over the TLS connection, chosen over
// HTTP because the channel is long-lived and has exactly two verbs. It is
// cafe's side of a contract whose server half lands with box's own
// capability-machinery work (W1-P3); the protocol is written down here rather
// than discovered from a server, so both sides have something to be checked
// against from day one, and the failure when they disagree is a failed test
// on one side or the other rather than a protocol invented twice.
//
// The idle case is part of that contract, measured live before it was written
// down: a `next` with nothing queued is answered with the empty object `{}` —
// "nothing now" — and the client re-polls on the same connection. It is named
// here because it was the case nobody wrote, and the measured cost of that
// omission was a healthy channel logging a protocol violation and dropping a
// TLS connection every next_wait (see ErrIdle).
const (
	opNext = "next"
	opAck  = "ack"
)

// ErrIdle marks the healthy quiet case, and it is the server's own answer
// rather than a client invention: box's serve_next holds a `next` up to its
// next_wait and, with nothing queued, replies the empty object `{}` —
// "nothing for you, ask again". It is neither a failure nor a capability, so
// the daemon re-polls on the SAME connection: an idle channel is a channel
// working, not a reason to end the round and burn a fresh TLS connection
// every next_wait. Measured on Contabo 2026-10-15 — before this sentinel
// existed, `{}` was decoded as a capability with no id and the daemon logged
// "box sent a capability with no id" once per poll interval forever.
var ErrIdle = errors.New("box has nothing queued; the channel re-polls")

// dialTimeout bounds the TCP+TLS handshake. A dial that hangs is a round that
// never learns anything, and Run's backoff cannot start for a connection that
// has not failed yet.
const dialTimeout = 10 * time.Second

// tlsDialer is the real Dialer: mutual TLS to the control plane, with the box's
// client certificate loaded per dial from the KeySource.
type tlsDialer struct {
	keys KeySource
	// RootCAs is the trust anchor for the control plane's certificate. Nil means
	// the system pool, which is what a real box uses — box presents a
	// publicly-issued certificate. A test injects its own pool so it can speak
	// to a listener with a self-signed one.
	RootCAs *x509.CertPool
}

// Dial opens one outbound mTLS connection. The certificate comes from the key
// source AT THE DIAL, not at construction: the box key is loaded for the life
// of this connection and released when the connection is closed, and a dialer
// that carried it between calls would be a long-lived secret in memory.
func (d tlsDialer) Dial(ctx context.Context, id Identity) (ControlPlane, error) {
	cert, err := d.keys.Certificate(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the box key for one connection: %w", err)
	}
	addr, host, err := dialTarget(id.ControlPlane)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{
		// The box's client certificate: this is what makes the channel
		// MUTUAL. Box verifies it, and this is the only credential the
		// box presents.
		Certificates: []tls.Certificate{cert},
		ServerName:   host,
		RootCAs:      d.RootCAs,
		MinVersion:   tls.VersionTLS13,
	}}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mTLS to %s: %w", addr, err)
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("mTLS to %s: the dialer did not return a TLS connection", addr)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(tlsConn), bufio.NewWriter(tlsConn))
	return &tlsControlPlane{conn: tlsConn, rw: rw, dec: json.NewDecoder(rw)}, nil
}

// dialTarget turns the identity's control-plane fact into what the network
// needs: the host:port to dial and the hostname to verify the server against.
//
// The identity stores the URL form — `https://box.svc.anywaye.com`, the
// spelling `--box` and CAF_BOX_URL accept and the form already on every
// enrolled box — because that is what a human typed, what `cafe boxd
// --report` shows, and what the enrollment HTTP client needs. The dial needs
// a bare host:port, so the translation happens HERE, where a stored fact
// becomes a network address, rather than in the writer: a writer that
// normalised would leave every already-written identity undialable until a
// re-enrollment, while a dialer that accepts both heals those boxes with a
// binary upgrade alone. The scheme and any path are irrelevant to the dial —
// the channel is always TLS over TCP — so they are stripped.
//
// A URL with NO port is the deployed form, not a mistake: the control plane
// is `https://box.svc.anywaye.com`, exactly what `cafe box enroll --box` is
// documented to take and stores verbatim, and a dialer that refused it meant
// a box enrolled exactly as documented could never report. So a portless URL
// dials the scheme's default — 443 for https, 80 for http. Those are the
// only two schemes whose default is worth guessing: any other scheme with no
// port, and a bare host with neither scheme nor port, are still refused with
// the same sentence, because they are enrollments that were read wrong and
// naming the field beats a tls error about the hostname.
func dialTarget(controlPlane string) (addr, host string, err error) {
	raw := strings.TrimSpace(controlPlane)
	if raw == "" {
		return "", "", fmt.Errorf("control plane %q is not host:port", controlPlane)
	}
	if strings.Contains(raw, "://") {
		u, uerr := url.Parse(raw)
		if uerr != nil || u.Host == "" {
			return "", "", fmt.Errorf("control plane %q is not host:port", controlPlane)
		}
		host, port, serr := net.SplitHostPort(u.Host)
		switch {
		case serr != nil && isMissingPort(serr):
			// The URL named no port. That is the deployed form rather
			// than a mistake — see the comment above — so the scheme's
			// default is dialed; a scheme with no known default is
			// refused rather than sent to a port nobody chose.
			def, ok := defaultSchemePort(u.Scheme)
			if !ok {
				return "", "", fmt.Errorf("control plane %q is not host:port", controlPlane)
			}
			// SplitHostPort leaves the host empty when the port is
			// missing; Hostname() is the bracket-free host — the form
			// JoinHostPort expects for an IPv6 literal.
			host = u.Hostname()
			return net.JoinHostPort(host, def), host, nil
		case serr != nil || host == "" || port == "":
			// A host malformed in some way other than lacking a port, or
			// none at all — an enrollment that was read wrong, named
			// rather than handed to a tls handshake as a hostname error.
			return "", "", fmt.Errorf("control plane %q is not host:port", controlPlane)
		}
		return net.JoinHostPort(host, port), host, nil
	}
	host, port, serr := net.SplitHostPort(raw)
	if serr != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("control plane %q is not host:port", controlPlane)
	}
	return net.JoinHostPort(host, port), host, nil
}

// isMissingPort reports whether err is SplitHostPort's "there was no port in
// the address" answer, as opposed to some other way the host was malformed —
// too many colons, a bracket in the wrong place. Only the first is a port the
// scheme can supply; the second is an address that was never dialable.
func isMissingPort(err error) bool {
	var addrErr *net.AddrError
	return errors.As(err, &addrErr) && addrErr.Err == "missing port in address"
}

// defaultSchemePort is the port a portless URL dials: the scheme's own
// default, which is what a browser, a curl and a human typing into `--box`
// all mean. The table is deliberately two rows — a scheme whose default is
// not in it has no port the dialer can honestly guess, so it is refused.
func defaultSchemePort(scheme string) (string, bool) {
	switch scheme {
	case "https":
		return "443", true
	case "http":
		return "80", true
	default:
		return "", false
	}
}

// NewTLSDialer is the real Dialer for a box: mutual TLS to the control plane the
// enrolled identity names, with the box's certificate loaded per dial.
func NewTLSDialer(keys KeySource) Dialer {
	return tlsDialer{keys: keys}
}

// tlsControlPlane is one connection speaking the line protocol.
type tlsControlPlane struct {
	conn *tls.Conn
	rw   *bufio.ReadWriter
	// dec is one decoder for the connection's life. A decoder per read would
	// work by luck on a request-response channel — the server cannot send the
	// next line before the next request — but one decoder is the honest shape:
	// it cannot read ahead past the newline it was asked for and drop the rest.
	dec *json.Decoder
}

// request is one line the box sends.
type request struct {
	Op  string `json:"op"`
	Ack *Ack   `json:"ack,omitempty"`
}

// Next sends the next request and reads one capability. It honors the
// context's deadline as a read deadline, because a Next with no deadline is a
// Next that can hang forever on a connection box has quietly dropped —
// and a hung round is a box that looks alive and redeems nothing.
//
// The idle case is decided on the LINE, before the capability decode, and the
// discriminator is deliberate: a line that names NO field at all is box saying
// "nothing now" and is ErrIdle, while a line that names fields but no id is
// still the anomaly it always was — a capability-shaped answer nobody can
// name cannot be acked, and answering it with a quiet re-poll would hide a
// real contract break. Decoding straight into Capability could not tell the
// two apart (`{}` is the zero value, and so is a capability with its fields
// missing), which is why the answer is read as a map of field names first.
func (c *tlsControlPlane) Next(ctx context.Context) (Capability, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return Capability{}, err
		}
	}
	if err := c.send(request{Op: opNext}); err != nil {
		return Capability{}, err
	}
	var answer map[string]json.RawMessage
	if err := c.read(&answer); err != nil {
		return Capability{}, err
	}
	if len(answer) == 0 {
		return Capability{}, ErrIdle
	}
	// The same bytes, now read as a capability: the map decode above is what
	// separates idle from capability-shaped, and this decode is the one that
	// fills the fields — unchanged from before ErrIdle existed, so a real
	// capability decodes exactly as it always did.
	raw, err := json.Marshal(answer)
	if err != nil {
		return Capability{}, fmt.Errorf("re-encode box's answer: %w", err)
	}
	var cap Capability
	if err := json.Unmarshal(raw, &cap); err != nil {
		return Capability{}, fmt.Errorf("decode the capability: %w", err)
	}
	if cap.ID == "" {
		return Capability{}, errors.New("box sent a capability with no id; a capability nobody can name cannot be acked")
	}
	return cap, nil
}

// Ack sends one capability's report and reads the acknowledgement. A failure
// is returned rather than swallowed: the daemon's round ends on it and
// reconnects, which is how box learns the connection is gone.
func (c *tlsControlPlane) Ack(ctx context.Context, ack Ack) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	if err := c.send(request{Op: opAck, Ack: &ack}); err != nil {
		return err
	}
	var acked struct {
		OK bool `json:"ok"`
	}
	if err := c.read(&acked); err != nil {
		return err
	}
	if !acked.OK {
		return errors.New("box refused the ack")
	}
	return nil
}

// Close ends the connection and, with it, the TLS session that holds the box
// key. The release is the reference drop: the TLS stack owns the last copy,
// and Close is where the daemon lets it go.
func (c *tlsControlPlane) Close() error {
	return c.conn.Close()
}

// send writes one request as a line. The write is flushed here because a
// buffered writer that is never flushed is a request box never sees.
func (c *tlsControlPlane) send(req request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}
	if _, err := c.rw.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write the request: %w", err)
	}
	return c.rw.Flush()
}

// read decodes one line of JSON from the connection. The JSON decoder's own
// framing is the line framing, which is why the protocol is one object per
// line rather than length-prefixed: no second parser to disagree with the
// first.
func (c *tlsControlPlane) read(into any) error {
	if err := c.dec.Decode(into); err != nil {
		return fmt.Errorf("read the response: %w", err)
	}
	return nil
}
