// Package setup is `cafe box setup <host>`: the one command that takes a bare
// host to a running box daemon, which is kamal `setup` parity for the box side
// of the platform.
//
// # What the command is
//
// `cafe box enroll` enrols a box that ALREADY has cafe on it. `cafe box setup`
// is the command before that: it SSHs into a host nobody has touched,
// bootstraps Docker the kamal way, installs the cafe binary, runs the
// enrollment ON THE HOST, writes the daemon's identity and unit, starts the
// daemon and verifies it answered. kamal's `setup` is the same idea — "install
// Docker on the destination, then hand it to deploy" — and the precedence in
// this package is kamal's precedence: check first, and only act on what the
// check found missing.
//
// The four rules that hold it together:
//
//   - A step is a CHECK and an APPLY, and the check runs first every time.
//     There is no step in this package that acts without first asking the host
//     whether it is needed, which is why a second `cafe box setup` on a host
//     that has had one runs nothing at all.
//   - Nothing secret crosses the Host seam twice. The capability is handed to
//     Run as stdin bytes — never as an argument, never into a file, never into
//     an environment variable — and the test walks every command and every
//     write the run performed looking for it.
//   - The controller keeps NO copy of anything the host ends up holding. The
//     keypair is generated on the host by the host's own `cafe box enroll`;
//     what crosses back is the box's non-secret facts and nothing else. The
//     test asserts the controller's config directory is untouched afterwards.
//   - A step that cannot be applied is a REFUSAL that names it, never a skip.
//     A bootstrap that quietly continued past a missing Docker is a bootstrap
//     whose log says "done" over a box that cannot run a container.
//
// # The seam
//
// Host is an interface with two methods — run a command, write a file. Every
// fact this package needs about a box arrives through it, which is what makes
// the whole flow a table a test can drive: the test's host is a small
// simulator that actually performs the enrollment against a counting control
// plane, so the idempotence proof and the single-redemption proof are run
// against the real command tree with no SSH, no network and no sleeps.
//
// The real Host is SSHHost, which speaks the local `ssh` binary through
// os/exec rather than implementing the SSH protocol itself. That is a decision
// rather than an omission: the repository's dependency discipline
// (AGENTS.md's "packages with dependencies" paragraph) is the reason
// internal/box and the reclamation path carry no dependencies at all, and an
// SSH client library would be the fourth direct dependency in the tree for a
// transport the operator's own `ssh` already speaks — with their agent, their
// known_hosts, their ProxyJump and their config file. Delegating to `ssh`
// costs one process per command and inherits all of that for free.
//
// Password authentication is the one thing `ssh` will not do without a
// terminal. SSHHost answers it the way the protocol intends: a tiny
// SSH_ASKPASS helper in a private 0700 directory, which reads the password
// from an inherited file descriptor. The password therefore never reaches
// argv, never reaches an environment variable and never reaches a file — the
// same pipe discipline the capability gets, applied to the operator's own
// credential.
//
// # What this package deliberately is not
//
// It is not the substrate installer. kit's `install.sh` owns the CLUSTER —
// k3s, Traefik, cert-manager, the data plane — and this command owns the HOST:
// Docker, the binary, the daemon, the enrollment. The two meet at `cafe box
// apply`, which records a phase; a command that did both would be a command
// whose blast radius nobody could review.
//
// It is not a second enrollment path. `cafe box enroll` runs on the host,
// written by this package's author or by a person with a shell, and this
// package only decides WHEN to run it and with what on its stdin. A setup
// that reimplemented enrollment in the controller would put the box's private
// key on the machine that is supposed to be the least trusted one in the
// system.
package setup
