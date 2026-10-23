package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/anywaye/cafe/internal/boxd"
	"github.com/anywaye/cafe/internal/secret"
	"github.com/anywaye/cafe/internal/verify"
)

// The ops half of `cafe box verify`: what the box runs UNDER, as opposed to
// the box's report ON itself. The first half — identity, key, journal, phase
// — is internal/box's, and it answers "did anything happen". This file
// answers the two questions the box is asked every time it opens its mouth:
//
//   - WHICH SECRETS ARE ON ME. The names come from the inventory the kit
//     installer's SECRET RECORD declares (internal/secret — the same table
//     `cafe secret inventory` prints), the presence comes from the
//     observation document's per-service settings NAMES, and the sealing
//     comes from the envelope names the box daemon was handed. Nothing here
//     reads a value; the observation carries names and nothing else, which
//     is why this file can print the whole answer without vetting it.
//   - WHICH HEALTH SIGNALS ARE OFF. The three the edge frets about, read
//     out of the same observe.report document the edge polls, per machine.
//
// WHY THE OBSERVATION IS A FILE ARGUMENT. `cafe boxd`'s job on the box is to
// answer over the mTLS channel, and the verification the CONTROL PLANE sees
// is the document that arrives there. A CLI that assembled its own reading
// would be a second opinion nobody asked for, so the argument is the
// document the box answered with and the verifier judges exactly that —
// which is also how `cafe failure check [<observation.json>]` already works,
// and one idiom for two commands is one idiom to learn.
type opsVerifyOptions struct {
	// observe is the observe.report document to verify against.
	observe string
	// stateDir is where the minted-secret ledger lives.
	stateDir string
	// mint names one secret to record as minted by this box, with the value
	// read from stdin.
	mint string
}

func (o *opsVerifyOptions) declare(fs *flag.FlagSet) {
	fs.StringVar(&o.observe, "observe", "", "the observe.report document the box answered with; without it, the ops half does not run")
	fs.StringVar(&o.stateDir, "state-dir", "", "where the minted-secret ledger lives; the default is the box's config directory")
	fs.StringVar(&o.mint, "mint", "", "record that this box minted <name>, reading the value from stdin; never put a secret in argv")
}

// readObserveReport reads the document and refuses, by name, a file that is not
// one. A verification run against a misread observation would answer a
// question nobody asked.
func readObserveReport(path string) (boxd.Observation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return boxd.Observation{}, fmt.Errorf("read the observation document: %w", err)
	}
	var obs boxd.Observation
	if err := json.Unmarshal(raw, &obs); err != nil {
		return boxd.Observation{}, fmt.Errorf("read the observation document: %s is not an observe.report document: %w", path, err)
	}
	return obs, nil
}

// mintedValue is the one place a secret value enters this command, and it
// arrives on STDIN rather than in argv because argv is visible to every other
// process on the box through /proc — the same discipline internal/secret's
// apply document holds itself to. It is read as one line, trimmed, and passed
// straight to the ledger, which keeps a fingerprint and drops the bytes.
func mintedValue(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("--mint needs the value on stdin; pipe it, and never put it in argv where every process on the box can read it")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read the minted value from stdin: %w", err)
		}
		return nil, fmt.Errorf("--mint read an empty value from stdin; a minted secret with no value is a ledger entry that proves nothing")
	}
	value := bytes.TrimRight(sc.Bytes(), "\r\n")
	if len(value) == 0 {
		return nil, fmt.Errorf("--mint read an empty value from stdin; a minted secret with no value is a ledger entry that proves nothing")
	}
	return append([]byte(nil), value...), nil
}

// secretsProbe is the cross-reference: the inventory's expected names, the
// observation's per-service settings NAMES, the daemon's envelope names.
// Three sources and one answer per name, which is what lets the report say
// "held on the box" and "sealed for the control plane" as two separate facts.
//
// Two rules hold it together, and both are about not reporting a fact nobody
// has:
//
//   - A SERVICE THE OBSERVATION NEVER READ IS UNREAD, NOT MISSING. The
//     observation's settings are keyed by service, so an entry whose
//     Deployment is absent from them was never looked at. Reporting it as
//     missing would turn a box that never asked into a box that is broken —
//     the same failure internal/failure's checker refuses to produce, one
//     level up. When NOTHING was read the probe is empty and the verifier
//     answers secrets_unread, which is the honest answer.
//   - A KEY THAT IS CONFIGURATION IS NOT A SECRET. The inventory's
//     Configuration provenance exists for exactly the entries that carry no
//     value at all — the mail adapter, the SMTP host and port — and a
//     verifier that demanded them would demand something that cannot exist.
//
// An entry that owns no Deployment — the Redis password, muse's vault key,
// the Postgres role names — is matched against every name the observation
// carries, because it is not one service's to set.
func secretsProbe(obs boxd.Observation) []verify.SecretInput {
	set := map[string]bool{}
	read := map[string]bool{}
	for svc, names := range obs.Settings {
		read[svc] = true
		for _, n := range names {
			set[n] = true
		}
	}
	sealed := map[string]bool{}
	for _, n := range obs.SealedSecrets {
		sealed[n] = true
	}

	var inputs []verify.SecretInput
	known := map[string]bool{}
	for _, entry := range secret.Inventory() {
		if entry.Provenance == secret.Configuration {
			continue
		}
		if entry.Deployment != "" && !read[entry.Deployment] {
			continue
		}
		known[entry.Name] = true
		if entry.Deployment == "" && entry.Namespace == "" && entry.Secret == "" {
			// muse's vault key: the inventory records no namespace and no
			// Secret object for it, because muse reads it per request out of
			// its own vault. It never becomes a cluster Secret, so the
			// settings surface cannot carry its name — and the verifier
			// reports WHERE it lives rather than demanding an object that
			// cannot exist.
			inputs = append(inputs, verify.SecretInput{
				Name:      entry.Name,
				Class:     string(entry.Class),
				Elsewhere: "the inventory records no namespace and no Secret object for it, so it is not on the cluster's Secret surface at all; muse reads it per request out of its own vault",
			})
			continue
		}
		inputs = append(inputs, verify.SecretInput{
			Name:    entry.Name,
			Present: set[entry.Name],
			Sealed:  sealed[entry.Name],
			Class:   string(entry.Class),
		})
	}
	// A name the box holds that the inventory does not know is a fact nobody
	// can classify, and it is still a fact worth reporting: the kit
	// installer's record is not the universe.
	for _, n := range sortedKeys(set) {
		if !known[n] {
			inputs = append(inputs, verify.SecretInput{Name: n, Present: true, Class: "unclassified"})
		}
	}
	for _, n := range sortedKeys(sealed) {
		if !known[n] {
			inputs = append(inputs, verify.SecretInput{Name: n, Sealed: true, Class: "unclassified"})
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name < inputs[j].Name })
	return inputs
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runOpsVerify is the ops half of the verb. It returns the verification and
// whether one was asked for at all, so the caller keeps its existing shape
// when no flag names the ops half — a verb that changed its output because a
// default moved is a verb that broke a script.
//
// ANY of the three flags asks for it, and so does the environment variable:
// the state directory is where the ledger lives, so naming it is asking the
// ops question. The default state directory is the box's own config
// directory only once the ops half is already running.
func runOpsVerify(opts *opsVerifyOptions, env *Env, stdin io.Reader, boxID string) (*verify.Verification, bool, error) {
	if opts.observe == "" && opts.mint == "" && opts.stateDir == "" && os.Getenv("CAF_BOX_STATE_DIR") == "" {
		return nil, false, nil
	}

	stateDir := opts.stateDir
	if stateDir == "" {
		if fromEnv := os.Getenv("CAF_BOX_STATE_DIR"); fromEnv != "" {
			stateDir = fromEnv
		} else {
			dir, err := defaultConfigDir()
			if err != nil {
				return nil, true, err
			}
			stateDir = dir
		}
	}
	ledger, err := verify.LoadState(stateDir)
	if err != nil {
		return nil, true, err
	}

	if opts.mint != "" {
		value, err := mintedValue(stdin)
		if err != nil {
			return nil, true, err
		}
		if _, err := ledger.Mint(opts.mint, mintClass(opts.mint), boxID, value, time.Now().UTC()); err != nil {
			return nil, true, err
		}
		if err := ledger.Save(stateDir); err != nil {
			return nil, true, err
		}
		say(env.Stderr, "recorded that box %s minted %s; the value was fingerprinted and dropped", boxID, opts.mint)
	}

	// The probe is only whole once an observation has been read; a mint on
	// its own records and reports, and says the health question was not
	// asked rather than answering it from nothing.
	probe := verify.Probe{BoxID: boxID, Now: time.Now().UTC(), Mint: ledger}
	if opts.observe != "" {
		obs, err := readObserveReport(opts.observe)
		if err != nil {
			return nil, true, err
		}
		probe.Observation = obs
		probe.Secrets = secretsProbe(obs)
	}

	v := verify.Check(probe)
	return &v, true, nil
}

// mintClass is the inventory's own word for the entry, read at mint time
// rather than stored as a second spelling of the class table. An entry the
// inventory does not carry is minted as rotatable, because a value a box
// mints is by definition a value a box can mint again.
func mintClass(name string) string {
	for _, entry := range secret.Inventory() {
		if entry.Name == name {
			return string(entry.Class)
		}
	}
	return string(secret.Rotatable)
}

// renderOpsVerify is the human half, on stderr, of an answer whose document
// is on stdout. Every line it prints is the report's OWN next line: who
// reads it, where, and what changes the answer — so a person who runs this
// verb and reads one line of prose does not have to SSH into the box and
// grep a log to find out what to do next.
func renderOpsVerify(env *Env, v *verify.Verification) {
	if v == nil {
		return
	}
	switch v.Status {
	case verify.StatusOK:
		say(env.Stderr, "box %s verifies: %d of %d secrets held, no signal needs attention", v.BoxID, heldSecrets(v), len(v.Secrets))
	case verify.StatusUnknown:
		say(env.Stderr, "box %s is unread rather than broken; see the reasons for what would fill each one", v.BoxID)
	default:
		say(env.Stderr, "health: degraded")
		for _, r := range v.Reasons {
			say(env.Stderr, "  %s", r.Detail)
			say(env.Stderr, "  %s reads this on %s.", r.Human.Owner, r.Human.Where)
			say(env.Stderr, "  what changes it: %s", r.Human.Action)
		}
	}
}

func heldSecrets(v *verify.Verification) int {
	n := 0
	for _, s := range v.Secrets {
		if s.Status == "on_box" {
			n++
		}
	}
	return n
}
