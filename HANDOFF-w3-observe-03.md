# HANDOFF-w3-observe-03 — what is left of OPS-P15 after the caf half

Read `REPORT-w3-observe.md` (the stream itself) and `REPORT-w3-observe-02.md`
(disk measured, drills per service). This file is only the remaining work, in
the order I would take it.

## Where the packet stands

The box side is complete and green: `internal/boxd/observe.go` holds the
document, `internal/cli/observe.go` + `internal/cli/boxd.go` hold the world's
readers, and `bin/prime` exits 0 with the suite at exactly its floor. The six
facts the packet row names are all measured, none of them guessed.

## What is left, in order

1. **Conductor's half — the other repository, and the actual blocker.** A route
   that receives one `Observation` JSON, and a mint that carries the account's
   pending `Release` records as `ObserveParams.Releases`
   (`CandidateRelease{name, version}` — only those two fields, deliberately).
   The box side is waiting on exactly that shape; see `refs/fact-conductor-caf.md`
   for the route inventory and where a new one goes.
2. **OPS-P16 (policy engine)** consumes this document. The per-service drill
   rows are what make its monthly-report output honest; `disk.used_percent` is
   the capacity input it classifies rather than the box.
3. **`caf box observe`** — a box-local rendering of the document without
   conductor. Useful, and a full subcommand: registry row in
   `internal/cli/command.go`, `stub_test.go`, README table, `wiring_test.go`,
   and a floor raise. Deliberately not started.
4. **Disk path.** `diskFact(path)` measures whatever it is handed and
   `rootDiskPath()` hands it the root. A box whose data lives on another mount
   wants that mount named; nothing chooses it yet, which is the seam.
5. **`CAF_BOX_RANGE` is still the range's source.** The right answer is
   enrollment writing the range into `boxd.Identity` (an optional field,
   backwards-compatible) and `observeRange()` reading it instead.

## If you touch these, run these

```
go test ./internal/cli/ -run 'Drill|Observe|Disk|RootDisk' -count=1
go test ./internal/boxd/ -count=1        # the type-graph Secret walk
gofmt -l . && go vet ./...
GOOS=windows go build ./...              # the disk probe is why this matters
bin/prime
```

And the mutation discipline this tree expects: break `serviceDrills` to return
nothing and watch two tests fail; break `diskFact`'s error path and watch the
named-failure test fail. Both were watched red in this packet.
