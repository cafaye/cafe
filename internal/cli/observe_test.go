package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/caf/internal/backup"
	"github.com/cafaye/caf/internal/boxd"
)

// The observe stream's pure facts, driven as tables, plus one integration that
// assembles a real Observation through the box-world's own readers — a real
// self-signed certificate on disk, a real backup drill journal, and the two
// env seams. The point of the integration is that the WHOLE machine-readable
// document is produced by the real path, marshalled, and read back, because the
// consumers (the policy engine OPS-P16, the monthly report OPS-P13) consume
// JSON and a report that does not marshal is a report nobody reads.

// TestPendingAgainstRangeMatchesInRangeNewer is the version-range match: a
// release is pending when it is inside the box's range AND strictly newer than
// what is deployed. Both halves matter — an already-deployed release is not
// pending, and a release outside the range is not this box's to apply.
func TestPendingAgainstRangeMatchesInRangeNewer(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		rangeText  string
		deployed   string
		candidates []boxd.CandidateRelease
		want       []string // "name@version", in order
	}{
		{
			name:      "in range and newer is pending; equal, older and out of range are not",
			rangeText: "^1.2.3",
			deployed:  "1.2.3",
			candidates: []boxd.CandidateRelease{
				{Name: "core", Version: "1.2.3"},
				{Name: "core", Version: "1.4.0"},
				{Name: "core", Version: "2.0.0"},
				{Name: "core", Version: "1.1.0"},
			},
			want: []string{"core@1.4.0"},
		},
		{
			name:       "a floor range admits everything above the deployed",
			rangeText:  ">=1.0.0",
			deployed:   "1.2.0",
			candidates: []boxd.CandidateRelease{{Name: "core", Version: "9.9.9"}},
			want:       []string{"core@9.9.9"},
		},
		{
			name:       "a candidate whose version does not parse is skipped, not fatal",
			rangeText:  "^1.2.3",
			deployed:   "1.2.3",
			candidates: []boxd.CandidateRelease{{Name: "core", Version: "not-a-version"}, {Name: "core", Version: "1.5.0"}},
			want:       []string{"core@1.5.0"},
		},
		{
			name:       "an empty range classifies nothing rather than calling everything pending",
			rangeText:  "",
			deployed:   "1.2.3",
			candidates: []boxd.CandidateRelease{{Name: "core", Version: "9.9.9"}},
			want:       nil,
		},
		{
			name:       "a deployed version that does not parse classifies nothing",
			rangeText:  "^1.2.3",
			deployed:   "latest",
			candidates: []boxd.CandidateRelease{{Name: "core", Version: "1.5.0"}},
			want:       nil,
		},
	}
	_ = now
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingAgainstRange(tt.rangeText, tt.deployed, tt.candidates)
			if len(got) != len(tt.want) {
				t.Fatalf("pending = %+v, want %v", got, tt.want)
			}
			for i, r := range got {
				if got := r.Name + "@" + r.Version; got != tt.want[i] {
					t.Errorf("pending[%d] = %q, want %q", i, got, tt.want[i])
				}
			}
		})
	}
}

// TestHealthStatusNamesEveryDegradedReason: health is a classification, and a
// degraded box says WHY — a healthy/degraded word with no reason is a report
// nobody can act on, which is the whole point of carrying reasons.
func TestHealthStatusNamesEveryDegradedReason(t *testing.T) {
	okCert := boxd.Certificate{Status: "ok"}
	expiringCert := boxd.Certificate{Status: "expiring"}
	passed := &boxd.DrillFact{Outcome: string(backup.DrillPassed)}
	failed := &boxd.DrillFact{Outcome: string(backup.DrillFailed)}

	tests := []struct {
		name       string
		cert       boxd.Certificate
		drill      *boxd.DrillFact
		wantStatus string
		wantReason string // a substring of one reason
	}{
		{"a passed drill and a long-lived certificate is healthy", okCert, passed, "healthy", ""},
		{"a failed drill is degraded and says so", okCert, failed, "degraded", "did not pass"},
		{"no drill is degraded: a box that has never proved a restore", okCert, nil, "degraded", "no backup drill"},
		{"an expiring certificate is degraded and names it", expiringCert, passed, "degraded", "close to expiry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := healthStatus(3, tt.cert, tt.drill)
			if h.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", h.Status, tt.wantStatus)
			}
			if h.Phase != 3 {
				t.Errorf("phase = %d, want 3", h.Phase)
			}
			if tt.wantReason == "" {
				if len(h.Reasons) != 0 {
					t.Errorf("healthy box carried reasons: %v", h.Reasons)
				}
				return
			}
			if len(h.Reasons) == 0 {
				t.Fatalf("a degraded box named no reason")
			}
			joined := strings.Join(h.Reasons, "; ")
			if !strings.Contains(joined, tt.wantReason) {
				t.Errorf("reasons %q do not name %q", joined, tt.wantReason)
			}
		})
	}
}

// TestCertStatusClassifiesRemainingLife: an expired certificate is a box that
// can no longer dial, an expiring one is a box with a window, and DaysLeft is
// allowed to go negative so a consumer can say HOW expired.
func TestCertStatusClassifiesRemainingLife(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		notAfter   time.Time
		wantStatus string
		wantSigned bool // DaysLeft negative
	}{
		{"forty days out is ok", now.Add(40 * 24 * time.Hour), "ok", false},
		{"ten days out is expiring", now.Add(10 * 24 * time.Hour), "expiring", false},
		{"in the past is expired, with negative days", now.Add(-5 * 24 * time.Hour), "expired", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, days := certStatus(tt.notAfter, now, certWarnWindow)
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
			if tt.wantSigned && days >= 0 {
				t.Errorf("DaysLeft = %d, want negative for an expired certificate", days)
			}
			if !tt.wantSigned && days <= 0 {
				t.Errorf("DaysLeft = %d, want positive", days)
			}
		})
	}
}

// TestTheBoxWorldObservesItself is the integration: the real box-world assembles
// a machine-readable Observation from a real certificate on disk, a real backup
// drill journal, the two env seams, and a conductor's candidate releases. It is
// marshalled and read back, because that is what the policy engine and the
// monthly report will do.
func TestTheBoxWorldObservesItself(t *testing.T) {
	now := time.Now()
	t.Setenv("CAF_BOX_RANGE", "^1.0.0")
	t.Setenv("CAF_DRILL_DIR", filepath.Join(t.TempDir(), "drills"))

	identityPath := writeObserveBox(t, "1.0.0", now.Add(40*24*time.Hour))
	// A drill that passed two days ago.
	writeObserveDrill(t, backup.DrillRecord{
		Service:    "identity",
		Engine:     "cnpg",
		Scratch:    "identity_drill",
		Outcome:    backup.DrillPassed,
		Reason:     backup.DrillReasonPassed,
		FinishedAt: now.Add(-48 * time.Hour),
	})

	world := newBoxWorld(boxd.Identity{BoxID: "box-test", Conductor: "conductor.example.com:443", Version: "1.0.0"},
		identityPath, filepath.Join(t.TempDir(), "boxd-state.json"))
	obs, err := world.Observe(context.Background(), boxd.ObserveParams{Releases: []boxd.CandidateRelease{
		{Name: "core", Version: "1.5.0"}, // in ^1.0.0, newer → pending
		{Name: "core", Version: "2.0.0"}, // out of range → not pending
	}})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if obs.SchemaVersion != boxd.ObservationSchemaVersion {
		t.Errorf("schema_version = %d, want %d", obs.SchemaVersion, boxd.ObservationSchemaVersion)
	}
	if obs.BoxID != "box-test" || obs.DeployedVersion != "1.0.0" {
		t.Errorf("box_id/deployed = %q/%q", obs.BoxID, obs.DeployedVersion)
	}
	if obs.VersionRange.Status != "ok" || obs.VersionRange.Range != "^1.0.0" {
		t.Errorf("version range = %+v, want ok ^1.0.0", obs.VersionRange)
	}
	if obs.Certificate.Status != "ok" || obs.Certificate.ExpiresAt == nil || obs.Certificate.DaysLeft == nil {
		t.Errorf("certificate = %+v, want ok with an expiry and days left", obs.Certificate)
	}
	if obs.Health.Status != "healthy" {
		t.Errorf("health = %+v, want healthy (ok cert, passed drill)", obs.Health)
	}
	if obs.LastDrill == nil || obs.LastDrill.Outcome != string(backup.DrillPassed) {
		t.Errorf("last drill = %+v, want the passed drill", obs.LastDrill)
	}
	if len(obs.PendingReleases) != 1 || obs.PendingReleases[0].Version != "1.5.0" {
		t.Errorf("pending releases = %+v, want exactly core 1.5.0", obs.PendingReleases)
	}
	// Disk is measured, not guessed: the root filesystem of the machine this
	// test runs on is real, and the numbers have to be consistent with each
	// other — a used percentage that is not the two byte counts' ratio is a
	// report a consumer cannot act on.
	if obs.Disk.Status != "ok" || obs.Disk.TotalBytes == 0 || obs.Disk.Path == "" {
		t.Errorf("disk = %+v, want ok with a path and a size", obs.Disk)
	}
	if obs.Disk.FreeBytes > obs.Disk.TotalBytes {
		t.Errorf("disk free %d exceeds total %d", obs.Disk.FreeBytes, obs.Disk.TotalBytes)
	}
	if obs.Disk.UsedBytes+obs.Disk.FreeBytes != obs.Disk.TotalBytes {
		t.Errorf("disk used %d + free %d != total %d", obs.Disk.UsedBytes, obs.Disk.FreeBytes, obs.Disk.TotalBytes)
	}
	wantPercent := 100 * float64(obs.Disk.UsedBytes) / float64(obs.Disk.TotalBytes)
	if diff := obs.Disk.UsedPercent - wantPercent; diff > 0.001 || diff < -0.001 {
		t.Errorf("disk used_percent = %v, want %v", obs.Disk.UsedPercent, wantPercent)
	}

	// Machine-readable: the document marshals, unmarshals and round-trips.
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"disk"`, `"total_bytes"`, `"used_percent"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("the JSON document is missing the key %s the consumers read", key)
		}
	}
	var back boxd.Observation
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the observation does not round-trip as JSON: %v", err)
	}
	if back.SchemaVersion != obs.SchemaVersion || len(back.PendingReleases) != 1 {
		t.Errorf("round-trip lost data: %+v", back)
	}
	for _, key := range []string{`"schema_version"`, `"box_id"`, `"deployed_version"`, `"health"`, `"certificate"`, `"pending_releases"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("the JSON document is missing the key %s the consumers read", key)
		}
	}
}

// TestTheBoxWorldObservesWithoutARangeHonestly: a box that has not been told
// its version range reports the ones it cannot classify as none, and says the
// range is unknown — rather than guessing and calling everything pending.
func TestTheBoxWorldObservesWithoutARangeHonestly(t *testing.T) {
	t.Setenv("CAF_BOX_RANGE", "")
	identityPath := writeObserveBox(t, "1.0.0", time.Now().Add(40*24*time.Hour))

	world := newBoxWorld(boxd.Identity{BoxID: "box-test", Version: "1.0.0"},
		identityPath, filepath.Join(t.TempDir(), "boxd-state.json"))
	obs, err := world.Observe(context.Background(), boxd.ObserveParams{Releases: []boxd.CandidateRelease{
		{Name: "core", Version: "9.9.9"},
	}})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.VersionRange.Status != "unknown" || obs.VersionRange.Detail == "" {
		t.Errorf("version range = %+v, want unknown with a reason", obs.VersionRange)
	}
	if len(obs.PendingReleases) != 0 {
		t.Errorf("pending releases = %+v, want none when the range is unknown", obs.PendingReleases)
	}
}

// TestDiskFactMeasuresARealFilesystem: the disk reading is a measurement, not
// a placeholder, and it is measured against a filesystem the test created so
// the machine's own fullness cannot decide the outcome. A box whose root is 98%
// full and a box whose root is 30% full must produce the same shape, differing
// only in the numbers — which is the property a policy engine classifies
// against.
func TestDiskFactMeasuresARealFilesystem(t *testing.T) {
	dir := t.TempDir()
	got := diskFact(dir)
	if got.Status != "ok" {
		t.Skipf("this platform could not measure %s: %+v", dir, got)
	}
	if got.Path != dir {
		t.Errorf("path = %q, want %q", got.Path, dir)
	}
	if got.TotalBytes == 0 {
		t.Errorf("total = 0 for a filesystem that exists: %+v", got)
	}
	if got.FreeBytes > got.TotalBytes {
		t.Errorf("free %d exceeds total %d", got.FreeBytes, got.TotalBytes)
	}
	if got.UsedBytes+got.FreeBytes != got.TotalBytes {
		t.Errorf("used %d + free %d != total %d", got.UsedBytes, got.FreeBytes, got.TotalBytes)
	}
	if got.UsedPercent < 0 || got.UsedPercent > 100 {
		t.Errorf("used_percent = %v, want it inside [0,100]", got.UsedPercent)
	}
}

// TestDiskFactNamesAFailureRatherThanGuessing: a path that cannot be measured
// reports `unknown` with the error's own words, and carries no bytes. A zero
// where a capacity belongs is a claim that the disk is empty, and that is the
// failure that would turn a policy engine's "act now" into a false alarm.
func TestDiskFactNamesAFailureRatherThanGuessing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere")
	got := diskFact(missing)
	if got.Status != "unknown" {
		t.Fatalf("status = %q, want unknown for a path that does not exist", got.Status)
	}
	if got.Detail == "" {
		t.Errorf("an unknown disk reading named no reason: %+v", got)
	}
	if got.TotalBytes != 0 || got.FreeBytes != 0 || got.UsedBytes != 0 || got.UsedPercent != 0 {
		t.Errorf("an unknown disk reading carried numbers: %+v", got)
	}
}

// TestRootDiskPathIsAbsolute: the path the observation measures is absolute,
// because the Win32 spelling of the question refuses anything else and a path
// that only works on one of the three release targets is a path that reports
// `unknown` on the other two.
func TestRootDiskPathIsAbsolute(t *testing.T) {
	got := rootDiskPath()
	if !filepath.IsAbs(got) {
		t.Errorf("root disk path = %q, want an absolute path", got)
	}
}

// TestServiceDrillsAreNewestPerServiceNewestFirst: the per-service grain is
// the monthly report's unit of reassurance — "every service restored, and the
// one that did not is named". A table drives three services with three
// different histories, and asserts one row each, newest first, with the service
// named and the age computed against the clock the test holds.
func TestServiceDrillsAreNewestPerServiceNewestFirst(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	recs := []backup.DrillRecord{
		{Service: "courier", Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed, FinishedAt: now.Add(-72 * time.Hour)},
		{Service: "billing", Outcome: backup.DrillFailed, Reason: backup.DrillReasonRefused, FinishedAt: now.Add(-5 * time.Hour)},
		{Service: "identity", Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed, FinishedAt: now.Add(-30 * time.Hour)},
	}
	got := serviceDrills(recs, now)
	if len(got) != 3 {
		t.Fatalf("drills = %+v, want one row per service", got)
	}
	if got[0].Service != "billing" || got[1].Service != "identity" || got[2].Service != "courier" {
		t.Fatalf("drills are not newest-first: %s, %s, %s", got[0].Service, got[1].Service, got[2].Service)
	}
	if got[0].Outcome != string(backup.DrillFailed) || got[0].Reason != string(backup.DrillReasonRefused) {
		t.Errorf("the newest row lost the failing verdict: %+v", got[0])
	}
	if got[0].AgeDays == nil || *got[0].AgeDays != 0 {
		t.Errorf("age days = %v, want 0 for a drill five hours ago", got[0].AgeDays)
	}
	if got[2].AgeDays == nil || *got[2].AgeDays != 3 {
		t.Errorf("age days = %v, want 3 for a drill three days ago", got[2].AgeDays)
	}
}

// TestServiceDrillsAreEmptyWhenNothingHasBeenDrilled: a box with no history
// produces no rows, not zero-value ones. A row with an empty service name and
// an empty outcome is a claim that some nameless service was drilled and
// succeeded, which is the shape of a lie nobody can act on.
func TestServiceDrillsAreEmptyWhenNothingHasBeenDrilled(t *testing.T) {
	if got := serviceDrills(nil, time.Now()); len(got) != 0 {
		t.Errorf("drills = %+v, want none for a box with no history", got)
	}
}

// TestTheBoxWorldObservesEveryServiceItHasDrilled is the per-service half of
// the integration: two real journals, two entries, and the box-level LastDrill
// points at the newer of them. It is the wire shape a monthly report reads.
func TestTheBoxWorldObservesEveryServiceItHasDrilled(t *testing.T) {
	now := time.Now()
	t.Setenv("CAF_BOX_RANGE", "")
	t.Setenv("CAF_DRILL_DIR", filepath.Join(t.TempDir(), "drills"))
	writeObserveDrill(t, backup.DrillRecord{
		Service: "courier", Engine: backup.EngineCNPG, Scratch: "courier_drill",
		Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed, FinishedAt: now.Add(-10 * 24 * time.Hour),
	})
	writeObserveDrill(t, backup.DrillRecord{
		Service: "billing", Engine: backup.EngineKamal, Scratch: "billing_drill",
		Outcome: backup.DrillFailed, Reason: backup.DrillReasonRefused, FinishedAt: now.Add(-2 * time.Hour),
	})

	world := newBoxWorld(boxd.Identity{BoxID: "box-test", Version: "1.0.0"},
		writeObserveBox(t, "1.0.0", now.Add(40*24*time.Hour)), filepath.Join(t.TempDir(), "boxd-state.json"))
	obs, err := world.Observe(context.Background(), boxd.ObserveParams{})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if len(obs.Drills) != 2 {
		t.Fatalf("drills = %+v, want one row per drilled service", obs.Drills)
	}
	if obs.Drills[0].Service != "billing" || obs.Drills[0].Outcome != string(backup.DrillFailed) {
		t.Errorf("drills[0] = %+v, want the newer failing billing drill first", obs.Drills[0])
	}
	if obs.LastDrill == nil || obs.LastDrill.Outcome != string(backup.DrillFailed) {
		t.Errorf("the box-level last drill = %+v, want the newest record's verdict", obs.LastDrill)
	}
	// The wire shape: the rows and the summary survive a marshal round trip.
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"drills"`) || !strings.Contains(string(raw), `"last_drill"`) {
		t.Errorf("the JSON document is missing the per-service drill rows or the box-level summary: %s", raw)
	}
}

// TestTheObserveStopsWhenTheConnectionIsGone: the assembly is bounded by the
// caller's context, because a report assembled for a connection that has
// already gone away is a report nobody will ack. A cancelled context is
// refused rather than answered with a stale document.
func TestTheObserveStopsWhenTheConnectionIsGone(t *testing.T) {
	t.Setenv("CAF_BOX_RANGE", "")
	identityPath := writeObserveBox(t, "1.0.0", time.Now().Add(40*24*time.Hour))
	world := newBoxWorld(boxd.Identity{BoxID: "box-test", Version: "1.0.0"},
		identityPath, filepath.Join(t.TempDir(), "boxd-state.json"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := world.Observe(ctx, boxd.ObserveParams{}); err == nil {
		t.Error("a cancelled context still produced an observation")
	}
}

// writeObserveBox writes an enrolled identity and a real self-signed client
// certificate with the given NotAfter, and returns the identity file path. The
// certificate is generated in-process so the test is hermetic — no fixture to
// expire, no key to keep.
func writeObserveBox(t *testing.T, version string, notAfter time.Time) string {
	t.Helper()
	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "box-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPath := filepath.Join(dir, "box.crt")
	keyPath := filepath.Join(dir, "box.key")
	writePEM(t, certPath, "CERTIFICATE", der)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)

	identityPath := filepath.Join(dir, "box-identity.json")
	doc := identityDocument{
		BoxID:       "box-test",
		Conductor:   "conductor.example.com:443",
		Version:     version,
		Certificate: "box.crt",
		Key:         "box.key",
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	if err := os.WriteFile(identityPath, data, 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	return identityPath
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("write pem %s: %v", path, err)
	}
}

// writeObserveDrill appends one drill record to the drill directory the env
// seam names, so the box-world's reader finds a real journal.
func writeObserveDrill(t *testing.T, rec backup.DrillRecord) {
	t.Helper()
	dir, err := backup.DefaultDrillDir()
	if err != nil {
		t.Fatalf("drill dir: %v", err)
	}
	store, err := backup.OpenDrillStore(dir)
	if err != nil {
		t.Fatalf("open drill store: %v", err)
	}
	if _, err := store.Append(rec); err != nil {
		t.Fatalf("append drill: %v", err)
	}
}
