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
// rather than an omission, and since AGENTS.md inverted its dependency rule it
// is a decision about fitness rather than about count: a good library is the
// default, so the hand-rolled transport owes its reason here. The candidate
// was golang.org/x/crypto/ssh, and cost is not the objection — it is already
// in this module's graph, so a direct import is a line in `require`, not a
// module tree. The objection is what a client library does not resolve: the
// operator's own ssh_config, ProxyJump, ControlMaster, agent and known_hosts,
// all of which `ssh` gives for free. Delegating costs one process per command;
// the write side of that channel can truncate a large payload (measured live
// on 2026-10-13), which is why a write lands on a sibling and the step after
// it verifies the installed bytes by sha256.
//
// Password authentication is the one thing `ssh` will not do without a
// terminal. SSHHost answers it the way the protocol intends: a tiny
// SSH_ASKPASS helper in a private 0700 directory, which reads the password
// from an inherited file descriptor. The password therefore never reaches
// argv, never reaches an environment variable and never reaches a file — the
// same pipe discipline the capability gets, applied to the operator's own
// credential.
//
// # The wipe
//
// `cafe box wipe` is the inverse and shares every rule above, applied
// backwards: its rows are read by wipe.go, its steps are check-and-apply so a
// second run is a no-op, and its first read-only question is a FINGERPRINT —
// does this host hold any of cafaye's artifacts? A host that does not is
// refused before the confirmation and before a single removal, which is what
// keeps the command from uninstalling a k3s it did not install. What it removes
// is a list of cafaye's own artifacts, never an `rm -rf` over a shared
// directory, and what it keeps — the operator's SSH keys and the deploy path's
// releases — is named in the result and in the help so the safety argument is
// readable rather than implied.
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
//
// # The architecture preflight
//
// Between the superuser probe and the cafe-installed step, the host is asked
// `uname -sm` and the answer decides WHICH BYTES cross the connection: see
// arch.go. It is the one place in this package where a controller is allowed
// to be a different machine from the host it is setting up, which means it is
// also the one place that can get it wrong — and the defect it was measured on
// got it wrong quietly, nine steps in, with a symptom nobody could act on.
package setup
