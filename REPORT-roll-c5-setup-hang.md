# REPORT-roll-c5-setup-hang: bound the binary write, so a stall becomes an error

## The measurement (from the brief, restated so the commit message can carry it)

Circle 10's first `cafe box setup contabo` ran **10 m 50 s** and never
finished. Output stopped at `8.0 MiB of 19.9 MiB written to contabo`. The
process was alive in state `S`, `lsof` showed nothing but pipes, the host was
healthy (`load average: 0.00, 0.30, 1.31`, ssh answering immediately), and no
partial file existed on the host. The retry wrote the whole 19.9 MiB in
**139 s** and reached `cafe-installed: applied` — intermittent, not a size or
bandwidth limit — then failed at enrollment with `capability has expired`,
because the capability's TTL is 15 minutes (box's `Capability::TTL_MINUTES`)
and the hang had eaten ten of them. A third run with a fresh capability
completed.

## What changed

**1. The write is bounded, per chunk and in total** (`internal/setup/write.go`).
`DefaultChunkTimeout = 2 min` bounds one 1 MiB chunk's silence; 19.9 MiB in
139 s is ~7 s/chunk on a healthy link, so 2 min is an order of magnitude of
headroom — a chunk quieter than that is stuck, not slow. `DefaultTotalTimeout
= 15 min` bounds the whole transfer at the capability's own TTL: a transfer
that outlives it has already spent the token it was setting up to use.
Expiry answers as a `writeStall` naming the chunk index, the bytes
acknowledged, the host and the elapsed time, e.g. `STALLED: chunk 2 of 3 made
no progress for 2m0s after 1048576 of 2097665 bytes acknowledged (elapsed
3m12s)`. The host-side sha256 gate and the atomic `mv` are untouched.

**2. The progress line carries elapsed time and throughput**
(`internal/cli/box_setup.go`). Same sentence, new tail: `1.0 MiB of 27.9 MiB
written to box.test (4s elapsed, 256.0 KiB/s)`. 300 KiB/s at growing elapsed
is a link to wait on; megabytes unmoving at growing elapsed is the stall.

**3. The write does NOT resume, and the refusal says so.** A failed transfer
removes the sibling and the hash gate proves the whole file or nothing, so
resuming would need verified offsets the host has no protocol for. Every
refusal (`writeStall`, `transportLost`, `writeMismatch`) now ends with: `The
transfer is not resumable: a retry restarts from zero and re-sends the whole
file.` Deliberate, stated, tested.

**4. The capability TTL is visible where it matters** (`internal/setup/setup.go`).
`CapabilityTTL = 15 min` restates box's number at the site that spends it.
When the enrollment apply is refused with `capability has expired`, the
refusal keeps the host's `mint a fresh token` line and appends the
arithmetic: `the capability lives about 15m0s and this run had already been
going … when enrollment was refused, so the token's minutes are spent`.

## The controls and their verdicts

All in `internal/setup/write_test.go` (stall/slow fixtures) and
`internal/setup/setup_test.go` (TTL), plus the progress assertion in
`internal/cli/box_setup_test.go`:

- GREEN: `TestAStalledChunkTimesOutInsteadOfHanging` — 3-chunk payload
  stalling on chunk 2 answers in ~the 150 ms test bound (not seconds) as a
  `writeStall` with `at=1`, `crossed=1 MiB`, chunk/bytes/elapsed/`restarts
  from zero` all in the message; sibling gone, destination untouched.
- GREEN: `TestASlowButSteadyTransferDoesNotTripTheDeadline` — every chunk
  acked just under the bound lands whole. The RED-adjacent control: a bound
  that killed this would be worse than the hang.
- GREEN: `TestTheWholeTransferBoundFires` — slow-but-steady past the total
  bound is refused naming the whole-transfer bound, elapsed, retry cost.
- RED (proven red, in-gate): `TestTheUnboundedWriteNeverAnswersAStallOnItsOwn`
  — the same stalled reader against a transfer with both bounds disabled (the
  write as it stands on master) never answers on its own; it returns only
  when the *caller* times out, and then as a dead transport, not a stall
  naming the chunk. The control half shows the bounded transfer against the
  identical fixture DOES name the chunk — the bound discriminates.
- GREEN: `TestAnExpiredCapabilityNamesTheElapsedTimeAndTheTTL`, with the
  control half asserting a Docker failure carries no TTL arithmetic.
- GREEN: `TestTheTransferProgressIsReadable` — both lines keep their shape
  and carry `elapsed` + `/s`.

Gate: `cafe gate floors --write` moved the suite floor 1427→1432 (5 new
tests) and re-pinned `caf.lock`; final `bin/prime`: 33 packages, 1432
top-level passes, 1273 subtest passes, 0 failures, 8 skips, exit 0;
`gofmt -l` clean, `go vet` clean.

## What was NOT done (per the packet's Do-not)

- No retry loop around a stall: a retry re-sends 19.9 MiB and hides the defect.
- No weakening of the sha256 gate or the `mv`; no resume protocol invented.
- No TTL raised: minutes are box's design; the run must not sit in a stall
  for ten of them, and now it cannot — it is refused at 2 min per chunk.
