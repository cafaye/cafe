package emitter

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"

	"github.com/cafaye/cafe/internal/telemetry"
)

// A real OTLP/gRPC collector, in-process, on a loopback port.
//
// WHY THIS EXISTS AND WHY IT IS A REAL SERVER. The first version of the wire test
// was a hand-rolled socket that read five bytes and replied with a fixed
// length-prefixed frame. It reported that a span had arrived — 1475 bytes, twice,
// once per command — and that was a LIE, and the lie is exactly what hid the TLS
// bug. The exporter was dialing with TLS against a plaintext collector, so what
// arrived on that socket was a ClientHello: 1475 bytes of handshake, counted as a
// span. The bytes could not be decoded as protobuf at all.
//
// So the server here is a gRPC server from the same proto packages the exporter
// speaks to, and it hands the test the decoded request. It cannot report success
// for bytes it did not understand, which is the only property that makes a wire
// test worth having.
//
// The spans are passed as the WIRE's own protobuf types rather than converted to
// the SDK's test stubs. That is a deliberate difference from the rest of this
// package's tests and it is the point: those assert on objects inside this
// process, and this one asserts on what a collector in another process received.
// Converting would import the SDK's interpretation of the bytes, which is the
// thing under test.
type collector struct {
	addr     string
	requests chan *coltracepb.ExportTraceServiceRequest
}

// endpoint is the address to hand the exporter, in the spelling core's contract
// uses: a scheme, then host:port. Plaintext, deliberately — it is what core's own
// default looks like, and using anything else here would be testing a transport
// cafe does not ship with.
func (c collector) endpoint() string { return "http://" + c.addr }

// next returns the next request the collector received.
//
// It blocks on a channel rather than sleeping, because the collector's handler is
// the event: a request arriving IS the signal, and the only thing that needs a
// deadline is the wait for an event that has not come — the same distinction
// `internal/cli/mcp_wire_test.go` draws.
func (c collector) next(t *testing.T, within time.Duration) *coltracepb.ExportTraceServiceRequest {
	t.Helper()

	select {
	case req := <-c.requests:
		return req
	case <-time.After(within):
		t.Fatalf("no OTLP request arrived within %v. The exporter opened no connection, or could "+
			"not complete a handshake — check the endpoint's SCHEME against the collector's "+
			"transport, since only https means TLS.", within)
		return nil
	}
}

// spans flattens a request into the wire's span messages, keeping the resource
// each was carried with, because identity is on the resource and a span alone
// cannot answer whether it arrived.
func spans(req *coltracepb.ExportTraceServiceRequest) []wireSpan {
	var out []wireSpan
	for _, resourceSpans := range req.GetResourceSpans() {
		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			for _, span := range scopeSpans.GetSpans() {
				out = append(out, wireSpan{span: span, resource: resourceSpans.GetResource()})
			}
		}
	}
	return out
}

// wireSpan is one span as the collector received it: the span's own fields plus
// the resource it was carried on.
type wireSpan struct {
	span     *tracepb.Span
	resource *resourcepb.Resource
}

// attribute returns a span attribute's string value and whether it was present.
func (w wireSpan) attribute(key string) (string, bool) {
	for _, kv := range w.span.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue(), true
		}
	}
	return "", false
}

// resourceAttribute returns a resource attribute's string value and whether it was
// present.
func (w wireSpan) resourceAttribute(key string) (string, bool) {
	for _, kv := range w.resource.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue(), true
		}
	}
	return "", false
}

// attributeNames, for a failure message that says which attributes DID arrive.
func (w wireSpan) attributeNames() []string {
	var out []string
	for _, kv := range w.span.GetAttributes() {
		out = append(out, kv.GetKey())
	}
	return out
}

func (w wireSpan) resourceAttributeNames() []string {
	var out []string
	for _, kv := range w.resource.GetAttributes() {
		out = append(out, kv.GetKey())
	}
	return out
}

// startCollector starts a collector on a free loopback port.
//
// The port comes from asking the kernel for port 0 rather than counting upwards
// from a constant. `internal/ports` exists in this repository precisely because a
// port chosen by a counter is a port two tests can both take, and this test must
// not be a second copy of that lesson.
func startCollector(t *testing.T) collector {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a loopback port for the collector: %v", err)
	}

	c := collector{
		addr:     listener.Addr().String(),
		requests: make(chan *coltracepb.ExportTraceServiceRequest, 8),
	}
	server := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(server, &collectorServer{requests: c.requests})

	// Started and stopped on the test's own lifetime, so a test that fails leaves
	// no listener behind for the next one to trip over.
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return c
}

type collectorServer struct {
	coltracepb.UnimplementedTraceServiceServer
	requests chan<- *coltracepb.ExportTraceServiceRequest
}

// Export publishes the decoded request.
//
// It returns an error rather than an empty acknowledgement when the request
// carried nothing, because acknowledging a request this server could not read is
// what made the hand-rolled version useless — an exporter dialing with the wrong
// transport gets told everything is fine and nobody notices for a year.
func (s *collectorServer) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if len(req.GetResourceSpans()) == 0 {
		return nil, errNothingReadable
	}
	select {
	case s.requests <- req:
	default:
		// A full channel means more requests than any test reads; dropping one is
		// better than blocking the exporter's flush inside a test.
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// errNothingReadable is what the collector answers when it cannot read what it
// was sent. It is deliberately not an empty success.
var errNothingReadable = errors.New("collector: the request carried no readable resource spans")

// wireInstall is an installation against a real collector, with the REAL
// exporter.
//
// It is separate from `recordingInstall` for a reason that is the whole reason
// this file exists: that helper INJECTS a capturing exporter, so a span produced
// through it never touches a socket. Every transport decision — the endpoint, the
// scheme, TLS or not, the batch processor's flush — sits below the injection
// point and is unreachable from it. A test that used it here would have passed
// with the exporter dialing with TLS at a plaintext collector.
func wireInstall(c collector) (Session, error) {
	return Install(context.Background(), Options{
		Lookup: func(name string) string {
			if name == telemetry.EndpointVariable {
				return c.endpoint()
			}
			return ""
		},
	})
}
