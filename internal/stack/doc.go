// Package stack is the box's answer to `lib/kamal/commands/proxy.rb`: where
// releases live on this box, which one is serving, and how a broken deploy
// gets undone without rebuilding anything.
//
// # What the box owns that the control plane does not
//
// Box can be unreachable, and a deploy that cannot be undone while the control
// plane is unreachable is not a rollback. So the stack keeps the one piece of
// state the deploy path needs on the box itself: the release history, and the
// last-good pointer inside it. Rollback reads a JSON file on local disk and
// writes a symlink. It makes no network call, asks no service for permission,
// and has no way to fail because something else is down.
//
// # The shape on disk
//
//	<root>/releases/<id>/release.json   one per release, written once
//	<root>/current -> releases/<id>      the traffic pointer, switched atomically
//	<root>/history.json                 every release, in order, plus last_good
//	<root>/logs/<id>.log                one release's own log
//
// The pointer is a RELATIVE symlink, so the whole tree can be moved, archived
// or rsynced elsewhere and still resolve. The switch is a symlink to a
// temporary name followed by a rename over the pointer: rename is atomic on
// the POSIX filesystems a box runs, and that is the entire mechanism by which
// a deploy has no downtime window. There is no moment at which `current` is
// missing or half-written, which is why the deploy can flip traffic on a live
// host without staging a second copy of anything.
//
// # Never overwrite in place
//
// A release directory is written once, with an exclusive create, and a
// re-stage of the same id is refused by name. Rollback is instant precisely
// because the artifact it goes back to is still sitting there, byte for byte,
// in the directory it was staged into; a stack that reused a directory would
// be a stack whose rollback depended on nobody having touched it. `Keep`
// prunes the tail, and never the current release or the last-good one.
//
// # What this package deliberately is not
//
// It is not a container runtime: how a release is started is a seam (Box)
// that the box's substrate implements, and a test drives a fake, so nothing
// here needs Docker to prove anything. It is not the edge: the live host and
// certificate state the deploy reads is the same seam, because the edge owns
// that state and a second copy of it here would be a copy that disagrees.
//
// It holds no credential. A release record carries the NAME of the secret set
// installed into it, never a value — the envelope is opened on the box and
// written where the release reads it, and the record above it is a sentence a
// stranger could read without learning anything. That is why the history file
// is world-readable rather than 0600: the release history is the product the
// operator reads, and a file nobody on the box can read is not an audit trail.
package stack
