# REPORT — OPS-P15 continued: disk is measured and the drills are per service

Branch: `ops/w3-observe`. This is the continuation of `REPORT-w3-observe.md`,
which landed the observe stream itself (route, document, version-range match).
Two commits from me: `628ed31` (disk) and `00eeb83` (per-service drills). The
manager merges; I did not push.

## What this attempt found already done

`git log` showed the previous attempt had not merely died mid-work — it had
finished: three commits (`e460deb` the packet, `3e015bc` the ratchet sweep,
`486a675` the report) were already on the branch and `git status` was clean.
So the first move was not to build the stream but to read the report and audit
its claims against the code. Both claims held; the two gaps it named as
"honest v0 gaps" are what this attempt closed.

## The blocker that was not a blocker

The previous report said disk could not be read: *"the daemon may not import
syscall … and the tree must cross-compile for Windows"*. The first half is true
and the conclusion does not follow from it. **`internal/boxd`'s `World` is
implemented in `internal/cli`** — `boxWorld` in `internal/cli/boxd.go` — and
`internal/cli` already owns three platform probes behind build tags
(`doctor_probe_darwin.go`, `doctor_probe_linux.go`, `doctor_probe_windows.go`),
one of which imports `syscall` and one of which runs `exec.Command`. The rule
constrains the *daemon*; the measurement was needed in the package that is
allowed to make it.

So `internal/cli/disk_probe_unix.go` (linux + darwin, `syscall.Statfs`) and
`disk_probe_windows.go` follow the doctor probes' existing split, and Windows
reaches `GetDiskFreeSpaceExW` through the same lazy-proc route
`GlobalMemoryStatusEx` takes — because the one call the standard library wraps
answers in clusters and sectors, and a caller that has to multiply its way to
bytes will eventually get the unit wrong.

Measured here, on this machine:

```
status=ok path=/ total=245107195904 free=25712783360 used=219394412544 used_percent=89.51
```

`boxd.Disk` gains the path, the two byte counts and the derived percentage —
stored rather than recomputed, for the same reason `DaysLeft` and `AgeDays`
are. No new dependency, no new config mechanism, no new file kind beyond the
two the package already had.

**The disk number is not a health reason, deliberately.** `healthStatus` still
classifies the certificate and the drill only. Two reasons: a threshold baked
into the box is a capacity limit the operator cannot see or tune, and the policy
engine (OPS-P16) is where classification belongs. The measurement on this
machine makes the second half concrete — a developer's laptop root at 89.5%
would report `degraded`, and a health word that is a function of how full the
operator's laptop is is a health word nobody trusts.

## The per-service drills

`LastDrill` was the newest record across the box, which is a summary of N
restores and not N restores proved. `Observation.Drills` now carries one
`ServiceDrill` row per service with a journal, newest first, each naming the
service, the verdict and the machine-matchable reason; `LastDrill` stays and
points at the record the first row does. The reader was split rather than
duplicated: `drillRecords()` is the newest record per journal,
`newestDrill()` picks the box-level winner, `serviceDrills()` flattens the set.
One directory walk.

## What a consumer gets now

One JSON document per box, all six facts the packet row names:

```json
{
  "schema_version": 1,
  "box_id": "box-test",
  "deployed_version": "1.0.0",
  "version_range": {"range": "^1.0.0", "status": "ok"},
  "conductor": "conductor.example.com:443",
  "collected_at": "2026-10-10T15:00:00Z",
  "health": {"status": "healthy", "phase": 0},
  "certificate": {"status": "ok", "expires_at": "…", "days_left": 40},
  "disk": {"status": "ok", "path": "/", "total_bytes": 245107195904,
           "free_bytes": 25712783360, "used_bytes": 219394412544,
           "used_percent": 89.51},
  "last_drill": {"outcome": "failed", "reason": "cafe-backup/drill-refused", …},
  "drills": [{"service": "billing", "outcome": "failed", …}, …],
  "pending_releases": [{"name": "core", "version": "1.5.0"}]
}
```

## Gate — exit codes I actually observed

- `go vet ./internal/cli/ ./internal/boxd/` → exit 0.
- `gofmt -l internal/` → no output.
- `GOOS=windows|linux|darwin go build ./...` → exit 0 for each. This one earns
  its place: the Windows disk file is the only reason it would not compile, and
  the release targets are exactly these three.
- `go test ./internal/cli/ -run 'Drill|Observe|Disk|RootDisk' -count=1` → ok,
  and `go test ./internal/boxd/ -count=1` → ok. `internal/boxd`'s type-graph
  walk (`secret_test.go`) still passes with `ServiceDrill` reachable from
  `Observation`.
- `go run ./cmd/cafe lock --verify .` → exit 0, 12 pinned files.
- **`bin/prime` → exit 0**, observed: `cafe: 25 packages, 1132 top-level passes,
  1218 subtest passes, 0 failures, 8 skips`. The floors are met with the suite
  at exactly its floor (1132), which is the ratchet saying the tree declares
  what it says it declares.

`gate.yml`'s suite floor moved twice, in the same commits as the tests that
moved it: 1125 → 1128 (the disk reading's three tests) → 1132 (the drills' four
tests). `caf.lock` re-pinned in both.

## Mutation checks run (a check nobody has watched fail might not work)

| Mutation | Caught by |
|---|---|
| `serviceDrills` returns nothing | `TestServiceDrillsAreNewestPerServiceNewestFirst` and `TestTheBoxWorldObservesEveryServiceItHasDrilled`, both with `drills = []` |
| `diskFact` returning the zero reading on a statfs error | the named-failure test's byte assertions |

## Deliberately not done

- **`cafe box observe` as a verb.** A box-local rendering of the document
  without conductor would be a genuinely useful operator tool, and it is a new
  subcommand: registry row, `stub_test.go`, README table, `wiring_test.go`,
  another floor raise. Not in an hour that still had a report to write.
- **An `observe` tool on the MCP surface.** `internal/mcp`'s six remote tools
  read the box through their own `World` seam; the observation is a document
  over the mTLS channel, not an MCP answer. Crossing the two is a seam decision,
  not a wiring job.
- **Disk pressure as a health reason** — above, with the measured reason.
- **`CAF_BOX_RANGE` is still the range's source.** Enrollment writing the range
  into `boxd.Identity` is the right answer and is not this packet's; the env
  seam is where the last report put it and I did not move it.

## Open, and the successor's first move

1. **Conductor (the other repo)** still owns the receive route and the mint
   that feeds `ObserveParams.Releases`. The box side is done and waiting on
   exactly that shape — `CandidateRelease{name, version}`.
2. **OPS-P16 (policy engine)** consumes this document. The per-service rows are
   what make its monthly-report output true; the disk number is the capacity
   input it classifies rather than the box.3. **Disk path**: the observation measures the root. A box whose data lives on
   another mount wants that mount named; the path is a parameter of
   `diskFact`, so the seam exists and nothing chooses it yet.

## One thing the operator should know

At 18:08, while this session was running, a process I did not start committed a
mid-flight snapshot of my work to this branch:
`127a0f0 recover(w3-observe-cafe): in-flight work after the provider dropped the
session` (author date 18:08:03, six files). I had run no `git commit` at that
point; my first commit was 30 minutes later. The workspace also holds
`/private/tmp/resume-wt/` worktrees from earlier packets, so the shape is an
automated session-recovery mechanism, but it fired inside a live session.

Two consequences worth writing down rather than glossing:

- **It is a snapshot, not my work.** Its `disk_probe_windows.go` is the version
  that calls `syscall.GetDiskFreeSpaceEx`, which does not exist in the standard
  library's Windows `syscall` package — I measured that exact line failing to
  compile in this directory minutes earlier, then rewrote the file to reach
  `GetDiskFreeSpaceExW` through a lazy proc. So `127a0f0` is an intermediate
  commit on this branch that does not build for `GOOS=windows`.
- **The branch tip is fine.** `628ed31`, `00eeb83` and `cc01bca` supersede it,
  and `bin/prime` exits 0 on the tip. I left the stray commit in place rather
  than rewriting history on a branch another process is committing to.
