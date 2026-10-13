# roll-c3-enroll-stdin — `cafe box enroll --token -` reads stdin

Branch `roll/c3-enroll-stdin`, worktree
`/Users/kaka/Code/any/moon/wt/roll-c3-enroll-stdin`. Four commits, none
pushed, none merged.

## The defect, measured

`cafe box setup <host>` runs the host's own enrollment over the SSH
connection and nowhere else — `internal/setup/plan.go`:
`/usr/local/bin/cafe box enroll --token - --box <url>`, `Stdin` is the
capability. On the host the command never opened the stream: it handed the
literal string `-` to `box.Store.Enroll`, which redeems it verbatim.

Reproduced against a loopback listener by building the merge base
(`b6d507d`) and running the command exactly as setup runs it:

```
BEFORE (b6d507d):  {"token":"-","public_key":"rwuxNMJ4koW0VMabmaBWIrDQx/z47qwB4omITrDOzsc"}
AFTER  (this tree): {"token":"cap_measured_from_the_pipe","public_key":"M7xkHn+uq2to2w41reqRpAzoBIcXoOMRP5vgddDO78U"}
```

Both runs answer `box does not know this capability` — the same sentence
the Contabo box produced, where box's redeem lookup was for
`sha256("-") = 3973e022e93220f9212c18d0d0c543ae7c309e46640da93a4a0314de999f5112`.

## Why the earlier "fix" did not fix it

`b67f561` ("fix(box): enroll reads the capability from stdin at --token -")
changed `internal/box/control_plane.go` (json.Number) and
`internal/setup/plan.go` (the `--box` flag). Nothing read stdin. The
message described a change that was not in the diff. This packet is that
change.

## What is done

1. `enrollCapability(env, fromFlag)` in `internal/cli/box.go`: `-` means
   read all of `readerOrStdin(env.Stdin)` — the seam `Run` already fills
   in, so a nil Stdin is the process's own stdin and a test hands the
   command a reader — then `TrimSpace`; a blank result is `errUsage` with
   the command's usage sentence, naming BOTH spellings now. A value that
   is not `-` is the flag's own, trimmed, and the stream is not opened.
   A failed read is a plain error (exit 1), mirroring `readSetupToken`.
2. Four tests, the first two of which fail on the tree before this packet:

   | test | red before | what it asserts |
   | --- | --- | --- |
   | `TestBoxEnrollReadsTheCapabilityFromStdinAtDash` | `the capability presented = "-"` | the value that reaches the control plane is the token on the pipe, trimmed |
   | `TestBoxEnrollWithAnEmptyPipeIsAUsageError` | enrolled a box from an empty pipe, exit 0 | exit 2, nothing attempted, control plane never called (which is what tells it apart from the store's own blank-token refusal, exit 1) |
   | `TestBoxEnrollWithAFlagTokenDoesNotReadStdin` | passes both sides, by design | the guard in the other direction: a reader that fails the test if it is read |
   | `TestTheSetupPipeCarriesTheCapabilityAndNothingElse` | — | the bytes the SETUP side writes |

3. The `internal/cli` setup simulator now performs the enrollment by
   running **the host's own `cafe box enroll`** through the real flag
   parser (`runHostEnroll`, args derived from the command the controller
   built via `hostEnrollArgs`), instead of calling `box.NewStore`
   directly. A sim that skipped the command would pass on the tree where
   the command sent the dash: the redemption count looked correct the
   whole time. With this change the e2e test went red on the old tree
   with `the host's control plane was presented [-]`.
4. `internal/setup/setup_test.go`'s capability-hygiene test now also
   asserts the pipe the capability travels on **is** the enroll command
   with `--token -` in it, so the two halves of the protocol keep
   describing each other.

## `readSetupToken` (brief item 3) — checked, both behaviours covered

`internal/cli/box_setup.go:162` reads `env.Stdin` for `""`/`-`,
`TrimSpace`s, and falls back to `$CAF_BOX_TOKEN`, refusing with `errUsage`
when neither has anything. The blank refusal was already covered by
`TestBoxSetupWithoutATokenIsAUsageError` (exit 2, host never contacted,
control plane never called). The **trim was not covered anywhere**, and
`TestTheSetupPipeCarriesTheCapabilityAndNothingElse` covers it now: it
hands the setup `  cap_pipe\n` and asserts the enroll pipe carried
exactly `cap_pipe`. This is the only place the setup side's trim is
observable — the host's enroll trims again, so an assertion about what
box was told would pass either way.

## Gates

`go build ./...`, `go vet ./...`, `gofmt -l .` (clean) and the focused
packages, all green:

```
ok  github.com/cafaye/cafe/internal/cli    65.573s
ok  github.com/cafaye/cafe/internal/setup    1.908s
ok  github.com/cafaye/cafe/internal/box      1.164s
```

`./bin/prime` on the whole tree:

```
cafe: 32 packages, 1361 top-level passes, 1247 subtest passes, 1 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
--- FAIL: TestEveryCitationPointsAtSomething (0.00s)  [internal/secret]
```

**The one failure is pre-existing and not this packet's.** It cites
`cafaye/kit/templates/substrate/install.sh:1239-1245` — a file in another
repository — and fails identically on the merge base `b6d507d` (proved by building and testing that commit in a scratch
worktree, since removed). It is also what keeps the `suite` floor proof
one short: on a green tree the count is 1362, exactly the floor this
packet raised it to. The fix belongs to whoever owns `internal/secret`'s
citation, or to `cafaye/kit` moving those lines back.

`gate.yml`: `suite` floor 1358 → 1362 (four top-level tests), `subtests`
floor 1245 → 1247 (the empty-pipe table's two cases), and `caf.lock`
re-pinned for `gate.yml`'s own hash, per AGENTS.md.

## Decisions

- **`readerOrStdin(env.Stdin)`, not `os.Stdin`.** The brief asks for it
  and it is the same seam `Run` uses; a nil `Stdin` therefore still means
  the process's stdin in a real invocation, and every test can hand the
  command a reader. `readSetupToken` takes a parameter instead, so it is
  driven directly; the enroll verb needed the whole `Env`.
- **A failed stream read is exit 1, an empty pipe is exit 2.** A stream
  that failed is a failure; a stream with nothing on it is a mistake. This
  is the same split `readSetupToken` makes.
- **One usage sentence for both cases** (missing flag, empty pipe), now
  naming `--token <capability>` and `--token -`. A second, pipe-specific
  sentence would be more precise and would have required the existing
  "no token" assertion in `TestBoxUsageErrors` to be reworded for no gain
  a reader would notice.
- **The simulator runs the real command.** This is the change that makes
  the CLI-level behaviour observable through the setup path at all. It
  cost nothing in runtime and turned an invisible defect into a red test.
- **`gofmt -w internal/box/control_plane.go`** — b67f561 left those struct
  tags misaligned, so the tree this packet inherited printed a file under
  `gofmt -l .`. Formatting only, no behaviour change.

## Deliberately not done

- The enroll protocol, the `-` convention and the setup-side stdin wiring
  are untouched — they were right.
- No substrate, box or other repository touched; the pre-existing
  `internal/secret` citation failure left alone for the same reason.
- No README row for a new subcommand (none added) — but the `box enroll`
  row now names the pipe, since the command's own `Usage` does.
- Nothing added to `boxLongHelp`'s EDGE section, and no `--json`/flag
  surface changes beyond the `-token` help text.

## The successor's first move

Nothing is outstanding. If the pre-existing `internal/secret` failure is
to be fixed, it is a citation into `cafaye/kit`; the packet's own gate
otherwise accounts for every number exactly (`declared 1370 - floor 1362
== expectedSkips 8`). A successor wanting more confidence could add the
same class of assertion to `box_verify`/`provider`'s stdin paths if any
ever appear — there are none today.
