// Package edge is the edge the box runs: host-based routing, TLS, and the
// release switch behind them.
//
// WHY IT IS HERE AND NOT IN A CHART. boxd owns the box
// (internal/boxd/doc.go) and the edge is the one thing on that box a customer
// has to look at. anywaye/notes/reference/names-urls-rollout-plan.md §4 is explicit that the box
// answer to kamal's per-box proxy is "boxd's embedded edge": one small thing
// on the box terminates TLS automatically, routes by hostname, and switches
// traffic between releases with zero downtime. The alternative — Traefik with
// an Ingress in front of it — is the shape the launch substrate uses, and it
// is the wrong shape here for one reason: it makes a customer learn what an
// ingress is. An Ingress is a Kubernetes object; a box is a host with
// hostnames on it.
//
// WHAT THE EDGE OWNS, AND WHAT IT DOES NOT. It owns exactly one thing — the
// edge spec: which hostname answers to which upstream, whether that upstream
// is healthy, and where `current` points. Everything else reads that record
// rather than keeping a second copy of it: `cafe box deploy` writes it, `cafe
// box rollback` flips it back, and boxd serves it through a route. A caller
// that kept its own host table would be a caller that could route a host the
// edge has already retired, which is a 404 nobody can explain.
//
// THREE RULES THE PACKAGE HOLDS.
//
// A release switch is a rename, not a restart. Releases live in directories
// under the box's own state directory and `current` is a symlink at the one
// that serves. Publishing writes the new directory and flips the symlink with
// rename(2), so the switch is atomic from a reader's point of view: a request
// either reaches the old release or the new one, never a directory that is
// half-written. Rollback is the same operation one step back, and it needs no
// control plane — see Rollback.
//
// The edge's answer is a fact about the box, not about a request. Hostname
// routing is the Host header, nothing else: no path routing, no header
// rewriting, no prefix tricks. Two hostnames on one box are two rows, and a
// hostname with no row is a 404 that names the hostname rather than a default
// upstream that serves the wrong app.
//
// NO SECRET IS EVER A FACT IN THIS PACKAGE. A TLS private key is a file the
// edge reads and never a field in any type it returns: the spec carries which
// mode the box terminates TLS in, never the key, never a token, never a
// Cloudflare credential. The edge's own health answer is about routing — see
// Health — so a boxd route that walked the edge's report could not leak a
// credential it never held.
//
// WHAT THIS PACKAGE IS NOT. It is not the daemon's route table
// (internal/boxd/route.go is the whitelist and stays the only way a
// capability reaches the box). It is not a container runtime and not a service
// mesh: the upstream a host routes to is an address the substrate published,
// and how that address came to be is another packet's business. And it is not
// a cluster Ingress: there is no YAML in this package's surface, no chart, and
// no namespace, because a customer who has to learn those words has been
// failed by the product rather than by the code.
package edge
