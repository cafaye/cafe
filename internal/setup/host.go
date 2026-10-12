package setup

import (
	"context"
	"os"
)

// Host is the box, reached over SSH. Two methods, and between them they are
// the whole surface this package has on a remote machine: run a command, and
// write a file.
//
// The interface is the rule as much as it is a seam. There is no method that
// reads a secret, no method that returns a key, and no method that takes a
// credential as a named argument — the one thing that could carry one is Run's
// stdin, which is bytes and not a string, so a caller cannot put a token in a
// position a log line would print. Everything above this is checkable: the
// test walks every call a run made and asserts the capability appears exactly
// once, as the stdin of the enrollment command and nowhere else.
type Host interface {
	// Run runs one shell command on the host and returns what it answered.
	//
	// A non-zero exit code is NOT an error: it is the answer. The plan's whole
	// design is that a check is a command whose exit status says whether the
	// thing is already done, and an interface that turned exit 1 into a Go
	// error would force every caller to distinguish "the check failed" from
	// "the transport failed" by parsing a message.
	//
	// stdin is handed to the command's standard input and is closed after it.
	// It is bytes rather than a string for the reason in the type's comment:
	// a capability travels here.
	Run(ctx context.Context, command string, stdin []byte) (Output, error)

	// Write puts a file on the host with the given mode, creating parent
	// directories. An error is a transport failure or a refusal by the host,
	// and the caller reports it as the step's failure rather than continuing.
	Write(ctx context.Context, path string, data []byte, mode os.FileMode) error
}

// Output is what a command on the host answered. It is a value rather than a
// pointer because every consumer of it copies the two strings into a log line,
// and because a nil *Output in a result document is a hole nobody notices.
type Output struct {
	// Code is the command's exit status. Zero means success.
	Code int `json:"code"`
	// Stdout is what the command printed, trimmed.
	Stdout string `json:"stdout,omitempty"`
	// Stderr is what the command complained about, trimmed. It is kept even on
	// success because the interesting half of a successful install is usually
	// on stderr, and because the checkout log is read by a person.
	Stderr string `json:"stderr,omitempty"`
}

// OK reports whether the command succeeded. A method rather than a field so
// the zero value is a failure rather than a lie about the host.
func (o Output) OK() bool { return o.Code == 0 }

// first lines, bounded: a command's output in a report is a sentence, not a
// transcript. The bound is here rather than at the print site so a caller that
// forgets cannot print ten megabytes of `curl` progress into a bootstrap log.
func (o Output) lines(n int) string { return clip(o.Stdout, n) }
