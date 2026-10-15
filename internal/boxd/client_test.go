package boxd

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The URL-form regression: the identity document carries `box_url` as a URL
// (`http://host:port`, the spelling `--box`/CAF_BOX_URL accept and the form
// `cafe box setup` writes), while the dial is TLS over TCP to a host:port.
// Before dialTarget, the dialer required a bare host:port and a URL identity
// failed every round with `control plane "http://…" is not host:port` — the
// live Contabo sentence. The dialer now normalises at the dial, so a URL
// identity connects and a bare host:port keeps working.
//
// The portless half of the same regression: the deployed control plane is
// `https://box.svc.cafaye.com` — no port, the form every doc and the CLI
// accept and store — so a URL with no port dials the scheme's default (443
// for https, 80 for http). What stays refused is a scheme with no known
// default and a bare host with neither scheme nor port: those are
// enrollments that were read wrong, and the sentence names the field.
func TestDialTargetNormalisesAURLToHostPort(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantAddr  string
		wantHost  string
		wantError string
	}{
		{name: "bare host:port still dials", in: "box.invalid:443", wantAddr: "box.invalid:443", wantHost: "box.invalid"},
		{name: "http URL to host:port", in: "http://127.0.0.1:4003", wantAddr: "127.0.0.1:4003", wantHost: "127.0.0.1"},
		{name: "https URL to host:port", in: "https://box.test:443", wantAddr: "box.test:443", wantHost: "box.test"},
		{name: "URL with trailing slash", in: "http://127.0.0.1:4003/", wantAddr: "127.0.0.1:4003", wantHost: "127.0.0.1"},
		{name: "URL with path", in: "http://127.0.0.1:4003/v1/capabilities", wantAddr: "127.0.0.1:4003", wantHost: "127.0.0.1"},
		{name: "URL with IPv6 host", in: "http://[::1]:4003", wantAddr: "[::1]:4003", wantHost: "::1"},
		{name: "bare IPv6 host:port", in: "[::1]:4003", wantAddr: "[::1]:4003", wantHost: "::1"},
		{name: "https URL with no port dials the deployed form's default", in: "https://box.svc.cafaye.com", wantAddr: "box.svc.cafaye.com:443", wantHost: "box.svc.cafaye.com"},
		{name: "http URL with no port defaults to 80", in: "http://127.0.0.1", wantAddr: "127.0.0.1:80", wantHost: "127.0.0.1"},
		{name: "https URL with path ignores the path", in: "https://box.test/v1/capabilities", wantAddr: "box.test:443", wantHost: "box.test"},
		{name: "an unknown scheme with no port is refused — only http and https have a default worth guessing", in: "ftp://box.test", wantError: "is not host:port"},
		{name: "bare host without a port is refused", in: "box.test", wantError: "is not host:port"},
		{name: "empty is refused", in: "", wantError: "is not host:port"},
		{name: "garbage is refused", in: "not a url at all", wantError: "is not host:port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, host, err := dialTarget(tt.in)
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("dialTarget(%q) = %q, want an error containing %q", tt.in, addr, tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("dialTarget(%q) error = %q, want it to contain %q", tt.in, err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("dialTarget(%q): %v", tt.in, err)
			}
			if addr != tt.wantAddr || host != tt.wantHost {
				t.Errorf("dialTarget(%q) = (%q, %q), want (%q, %q)", tt.in, addr, host, tt.wantAddr, tt.wantHost)
			}
		})
	}
}

// TestAURLIdentityDialsOverMutualTLS drives the real dialer against the real
// loopback listener with the URL form in the identity — the live box's
// document shape. It fails on the old dialer (which rejects any URL before
// touching the network) and passes now, proving a URL identity connects
// rather than merely parses.
func TestAURLIdentityDialsOverMutualTLS(t *testing.T) {
	pki := newTestPKI(t)
	cap := oneCapability("box.status")

	// One server per dial: the fake listener accepts a single connection,
	// so a second dial against the same server would hang rather than fail.
	for _, tc := range []struct {
		name string
		form func(addr string) string
	}{
		{"plain URL", func(addr string) string { return "http://" + addr }},
		{"URL with trailing slash", func(addr string) string { return "http://" + addr + "/" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newFakeServer(t, pki, cap)
			defer server.close()
			form := tc.form(server.addr())
			keys := &instrumentedKeys{cert: pki.box}
			dialer := tlsDialer{keys: keys, RootCAs: pki.pool}
			id := Identity{BoxID: "box-test", ControlPlane: form, Version: "test"}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ch, err := dialer.Dial(ctx, id)
			if err != nil {
				cancel()
				t.Fatalf("dial with identity %q: %v", form, err)
			}
			got, err := ch.Next(ctx)
			cancel()
			ch.Close()
			if err != nil {
				t.Fatalf("next over the URL-identity channel %q: %v", form, err)
			}
			if got.ID != cap.ID {
				t.Errorf("capability = %q, want %q (identity %q)", got.ID, cap.ID, form)
			}
		})
	}
}
