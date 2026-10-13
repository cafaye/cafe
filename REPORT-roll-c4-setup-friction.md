# REPORT — roll/c4-setup-friction

**Status:** both frictions addressed; the packet's own second half was found to be
a **no-op as committed** and fixed here; gate red only for the documented
pre-existing `internal/secret` failure.

**Worktree:** `/Users/kaka/Code/any/moon/wt/roll-c4-setup-friction`,
branch `roll/c4-setup-friction`, repo `cafaye/cafe`.

**Commits on this branch for this packet:**

- `2f0c615` — the previous attempt. Flag position fixed and the control-master
  transport introduced. Its disk measurement is sound in shape but its code did
  not do what the message says; see §3.
- `90bfdfa` — this attempt. The shared connection actually opens (`argsFor`), the
  guard now reads the transport, and the gate floors + `caf.lock` follow.

---

## 1. Friction 1 — flags after the host

**Decision: ACCEPT the interspersed form.** `cafe box setup contabo
-control-plane …` is the same command as `cafe box setup -control-plane …
contabo`. The reason is at the code site (`internal/cli/box_setup.go`, the
`Interspersed` comment) and the mechanism is in `internal/cli/command.go`.

Why accept and not improve the refusal: the misremembered-flag case is the only
real mistake in that form, and "wants 1 argument, got 3" names a shape rather
than the mistake. A flag that does not exist is now refused **as a flag**
(`flag provided but not defined: -controlplane`), a second subject is still a
usage error, and `--` still ends the flags. `parseArgs` loops the parse: one
positional is taken per stop, the rest is re-parsed, and an unknown flag
propagates as a flag error rather than being swallowed as an argument.

`Interspersed` is a **per-command opt-in**, not the default, because Go's `flag`
package stopping at the first positional is the right rule for a command that
forwards its tail to a child process (`cafe dev logs -f` must not have an
unknown `-f` become an error).

**Consistency check across the box verbs** (as the brief asked):

| verb | positional subject | Interspersed |
|---|---|---|
| `box setup` | `<host>` (required positional) | **yes** |
| `box deploy` | none — host is `--host`, or read from box files | no |
| `box logs` / `box verify` | none | no |
| `box apply` | optional `<phase>` (not a host) | no |
| `box rollback` | optional `<release-id>` (not a host) | no |
| `box publish` | `<release-id> <dir>` (not a host) | no |
| `box wipe` | does not exist at this commit | — |

Only `box setup` takes a positional **host**, so it is the only verb the brief's
rule reaches. The three verbs that take an optional non-host positional carry the
same latent friction and are named under §6 as deliberately out of scope.

**Proof at the CLI level** (`internal/cli/box_setup_test.go`): the two forms
produce the same document, the same host, one redemption, the same step order,
and the host runs the same `cafe box enroll --token - --box '<origin>'` — the
flag reached the host's command, so it was parsed and not tolerated. A misspelled
flag after the host is refused as a flag and names it; a second subject is a usage
error and the host is never contacted; an empty host is refused **before** stdin
is read.

Observed on the real tree (unresolvable host, so nothing is touched):

```
$ cafe box setup no-such-host.invalid --token x --control-plane http://127.0.0.1:4003 --dry-run --json
{ "host": "no-such-host.invalid", "dry_run": true, "steps": [ … superuser: REFUSED … ] }
```

No "wants 1 argument, got 3".

## 2. Friction 2 — the binary write

**Decision: one SSH control master across the chunks.** The bounded 1 MiB chunks,
the sibling file, the host's own `sha256sum` + `wc -c`, and the rename-on-match
are **unchanged**. Only how many times the transport says hello changed.

Candidates and why the others lost:

- **Bigger chunk:** there is no ceiling to raise by measurement. The original
  defect (17,625,600 of 29,250,507 bytes, zero exit) did not reproduce as a byte
  cap on re-measurement; a bound that moves ~7 MB between two runs is not a bound
  you raise. Also no throughput argument: a single unbounded stream measured
  96 s against ~93 s for the shared chunks — the whole truncation risk for ~3%.
- **A checksummed tar in one stream:** the same unbounded pipe write, better
  dressed — the exact shape that truncated. Rejected.
- **Control master:** the operator's own `ssh` already speaks it; the walk keeps
  `ControlMaster=no`, and only a multi-chunk `Write` shares one. The socket lives
  in a private 0700 directory of its own, and it is stopped on every path out
  (success and refusal).

`needsControl(n)` is `chunkCount(n) > 1`: a one-chunk payload (a unit file, an
identity document) opens no master.

## 3. The defect this packet had, and how it was caught

**The previous commit's control master was a NO-OP, and its measurement said so
once the number was taken on the shipped code.**

`SSHHost.run` built `sshArgs()` — which already carries `-o ControlMaster=no` —
and then **appended** `controlArgs(...)` with `-o ControlMaster=auto`,
`ControlPath`, `ControlPersist`. ssh honours the **first** `-o` for a repeated
option, so `no` won and no master ever opened. Verified directly:

```
$ ssh -o ControlMaster=no -o ControlMaster=auto -o ControlPath=/tmp/x … contabo true
ls /tmp/x   -> No such file or directory      # no master
$ ssh -o ControlMaster=auto -o ControlPath=/tmp/x … contabo true
ls /tmp/x   -> srw-------  ... s              # master
```

The test meant to guard it, `TestTheWritesPolicyDiffersFromTheWalksInOneLine`,
built its **own** write list (`baseArgs()+controlArgs`) while the transport built
`sshArgs()+controlArgs`, so the two never had to agree and it stayed green over a
transport that shared nothing.

**The fix:** `SSHHost.argsFor(sock)` is now the one function that builds the list
the transport hands to ssh — `sshArgs()` for the walk, `baseArgs()+controlArgs`
for the write — and both `run` and the policy test call it. The guard was watched
red by re-introducing the stacking inside `argsFor`:

```
control_test.go:201: the write policy stacked both ControlMaster values, and ssh honours the first:
  [ … -o ControlMaster=no -o ControlMaster=auto -o ControlPath=… -o ControlPersist=yes]
```

## 4. The numbers (real runs against `contabo`)

Method: a scratch harness that drives the **real** `internal/setup` code path
against `/tmp/cafe-bench/*` on the box, a deterministic 29,250,507-byte payload,
host-side `sha256` + byte count verified every run, then `/tmp/cafe-bench`
removed. "before" calls `SSHHost.Run` once per chunk (the pre-fix transport,
`ControlMaster=no`); "after" calls `SSHHost.Write` (the shipped path).

| run | transport | time (28 × 1 MiB, sha256 verified) |
|---|---|---|
| per-chunk, no master | `Run` per chunk, `ControlMaster=no` | **159.07 s** |
| as committed at `2f0c615` | `sshArgs()` + control appended (no-op) | **210.70 s** |
| **fixed (`argsFor`)** | `baseArgs()` + control (one master) | **95.82 s** |

The link has real variance: the two per-chunk runs (which share nothing) came out
159 s and 211 s — ~30%. The fixed run at 95.82 s sits well outside that band and
matches the previous attempt's own 92.8 s figure for the correctly-built master.
So: **~160–210 s down to ~96 s, about 1.7× to 2.2×, with the verification
untouched.** The previous attempt's 92.8 s figure is credible; its *code* was the
part that did not hold.

(Note for a successor: the previous attempt's message reported before=199.33 s /
after=92.8 s. This attempt re-measured all three lines above; the only line that
differs in kind is the committed-no-op one, which nobody had measured on the
shipped code.)

## 5. Tests

New, all green:

- `internal/cli/box_setup_test.go` — 4 CLI cases + 2 helpers: the two flag
  positions are the same command (document, host, redemption count, step order,
  and the host's own enroll argv); a misspelled flag is refused as a flag and the
  host is not contacted; a second subject is a usage error; an empty host is
  refused before stdin is read.
- `internal/cli/command_test.go` — `TestParseArgsTakesFlagsOnEitherSideOfAPositional`,
  a 10-case table (default vs interspersed, value flag between positionals,
  unknown flag still a flag, `-h` after the subject, `--`).
- `internal/setup/control_test.go` — 6 cases: the chunks share one connection and
  it is taken down exactly once; it is taken down on the refusal path too; a
  **truncated arrival is still refused through the shared connection** (the
  `shellHost.pipeCap` assertion, naming both hashes and both byte counts); the
  write's policy differs from the walk's in **one line** (this is the guard the
  bug slipped past — it now calls `argsFor`); one chunk opens no master; the
  socket is private and short.

Untouched and still biting (`internal/setup/write_test.go`): the measured
truncation refusal, the previous-binary-intact case, the mid-stream cut cases,
the sibling-only-opened-for-writing case, and the derived chunk count. A
regression to an unverified single write still goes red — the pipe-cap host
answers success and the transfer refuses.

## 6. What was deliberately NOT done

- **Did not loosen verification or drop the chunking.** The sha256 + byte count
  and the 1 MiB bound are exactly as they were; the tar-stream and bigger-chunk
  candidates were measured and rejected above.
- **Did not extend `Interspersed` to `box apply` / `box rollback` / `box publish`.**
  They take an optional non-host positional and carry the same latent friction
  (`cafe box rollback rel-1 --service api` still says "wants 1 argument, got 3").
  The brief scoped the fix to host-taking verbs and only `box setup` qualifies;
  extending to these three is a separate, testable change. A successor's first
  move is `Interspersed: true` on those three `Command` literals plus one table
  test.
- **Did not touch the walk's `ControlMaster=no`.** A master is a
  credential-bearing channel held open for a whole run; the walk's steps each need
  one command and keep their policy.
- **Did not change the box.** Only `/tmp/cafe-bench` scratch was written and it was
  removed afterwards.
- **Did not touch `cafaye/kit` or any other repo**, and spawned no other agent.

## 7. Gate

`gofmt -l .` clean; `go vet ./...` clean; `go run ./cmd/cafe lock --verify .`
clean (`matches 12 pinned file(s)`).

Raised the floors the previous commit's tests demanded but it did not raise:
`suite` 1370 → **1381** (1389 declared − 8 skips), `subtests` 1248 → **1258**.
`caf.lock` regenerated (`gate.yml` is the tree's one rule-bundle; its hash moved).

`./bin/prime`:

```
cafe: 32 packages, 1380 top-level passes, 1258 subtest passes, 1 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
--- FAIL: TestEveryCitationPointsAtSomething (0.00s)   [internal/secret]
exit: 1
```

**The one failure is pre-existing and not this packet's.** It is the
`internal/secret` citation check against `cafaye/kit/templates/substrate/install.sh`,
whose line numbers moved in kit; it is documented as pre-existing in
`REPORT-c3-rename-cafe.md`, `REPORT-roll-c3-boxd-identity.md` and
`REPORT-roll-c3-enroll-stdin.md`, and this packet touches no file in
`internal/secret`. The gate is red at base and red now for that one reason.

Before the lock was regenerated the gate also showed the two
`internal/ci` lock failures; those were this packet's and are fixed (§7).

## 8. Handoff / other findings

- The pre-existing `internal/secret` failure is the only red; if kit's
  `install.sh` is re-pinned or the citations re-measured, re-run the gate and the
  numbers in §7 move by one failure.
- Pre-existing and out of scope, noted so nobody re-discovers it: `boxVerbs`
  appends `boxEdgeVerbs`, which defines a second `"rollback"`; `lookupBoxVerb`
  returns the first, so the **stack** rollback (`internal/cli/box_deploy.go`) is
  what `cafe box rollback` dispatches to and the **edge** rollback
  (`internal/cli/box_edge.go`) is unreachable. Not touched here.
