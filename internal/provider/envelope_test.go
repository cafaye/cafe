package provider

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// generateBox is a real ed25519 keypair, so the conversion and the round-trip
// are exercised over the same bytes a cafe run would use.
func generateBox(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the box keypair: %v", err)
	}
	return pub, priv
}

// The conversion is the load-bearing piece: if ed25519PublicToX25519 does not
// produce the X25519 public key of the same keypair, then an envelope sealed
// to the converted public key cannot be opened with the converted private key
// and the whole scheme is a one-way hash. This test pins the conversion to the
// standard by deriving the X25519 public key from the converted PRIVATE key
// and asserting it equals the converted PUBLIC key — the property the standard
// conversion has and a wrong one does not.
func TestTheEd25519ToX25519ConversionIsTheStandardOne(t *testing.T) {
	pub, priv := generateBox(t)

	x25519Public, err := ed25519PublicToX25519(pub)
	if err != nil {
		t.Fatalf("convert the public key: %v", err)
	}
	x25519Private, err := ed25519PrivateToX25519(priv)
	if err != nil {
		t.Fatalf("convert the private key: %v", err)
	}

	// Derive the X25519 public key from the converted private key, the way
	// X25519 itself would, and assert it is the converted public key.
	private, err := ecdh.X25519().NewPrivateKey(x25519Private)
	if err != nil {
		t.Fatalf("load the converted private key: %v", err)
	}
	derived := private.PublicKey().Bytes()
	if string(derived) != string(x25519Public) {
		t.Errorf("the converted public key is not the X25519 public key of the converted private key\nconverted public: %x\nderived public:  %x", x25519Public, derived)
	}
}

// The round-trip is the proof the envelope is a real envelope and not a
// one-way hash: a credential sealed to the box's public key comes back out of
// the envelope when the box opens it with its private key.
func TestSealAndOpenRoundTrips(t *testing.T) {
	pub, priv := generateBox(t)
	credential := "smtp-password-123"

	envelope, err := Seal(credential, pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := Open(envelope, priv)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != credential {
		t.Errorf("round-trip returned %q, want %q", got, credential)
	}
}

// The envelope is the whole of what crosses the wire and the whole of what
// box stores, so it must not carry the credential in any recoverable
// form — not raw, not base64, not hex. This is the client half of boundary 6's
// tripwire, and it is asserted over the whole envelope document rather than
// field by field, because a value that reached a field nobody thought to check
// is exactly the case the tripwire exists for.
func TestTheEnvelopeCarriesNoFormOfTheCredential(t *testing.T) {
	pub, _ := generateBox(t)
	credential := "super-secret-smtp-password"

	envelope, err := Seal(credential, pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// The raw form, and the two encodings a leak would most plausibly take.
	forms := map[string]string{
		"raw":      credential,
		"base64":   base64.StdEncoding.EncodeToString([]byte(credential)),
		"hex":      hex.EncodeToString([]byte(credential)),
		"reversed": reverseString(credential),
	}
	for name, form := range forms {
		if strings.Contains(envelope, form) {
			t.Errorf("the envelope contains the credential's %s form", name)
		}
	}

	// And the envelope is the shape it claims to be: five fields, v and alg
	// the ones this package writes, the rest base64.
	var env Envelope
	if err := json.Unmarshal([]byte(envelope), &env); err != nil {
		t.Fatalf("the envelope is not JSON: %v", err)
	}
	if env.V != EnvelopeVersion {
		t.Errorf("v = %d, want %d", env.V, EnvelopeVersion)
	}
	if env.Alg != EnvelopeAlgorithm {
		t.Errorf("alg = %q, want %q", env.Alg, EnvelopeAlgorithm)
	}
	for _, field := range []string{env.Key, env.IV, env.Tag, env.Data} {
		if _, err := base64.StdEncoding.DecodeString(field); err != nil {
			t.Errorf("envelope field %q is not base64: %v", field, err)
		}
	}
}

// An envelope is sealed to ONE box. A second box's private key must not open
// it — that is the property that makes handing the envelope to box safe:
// box can store it, but only the box it was sealed to can read it.
func TestAnEnvelopeSealedToOneBoxCannotBeOpenedByAnother(t *testing.T) {
	pub, _ := generateBox(t)
	_, otherPriv := generateBox(t)

	envelope, err := Seal("credential", pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(envelope, otherPriv); err == nil {
		t.Errorf("a second box opened an envelope sealed to the first")
	}
}

// A tampered envelope fails the GCM auth rather than decrypting to something
// else. This is what makes the envelope tamper-evident: a flipped bit in the
// ciphertext is a refusal, not a different plaintext.
func TestATamperedEnvelopeIsRefused(t *testing.T) {
	pub, priv := generateBox(t)

	envelope, err := Seal("credential", pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(envelope), &env); err != nil {
		t.Fatalf("the envelope is not JSON: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		t.Fatalf("decode the data: %v", err)
	}
	data[0] ^= 0x01 // flip one bit
	env.Data = base64.StdEncoding.EncodeToString(data)
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("re-encode the tampered envelope: %v", err)
	}

	if _, err := Open(string(tampered), priv); err == nil {
		t.Errorf("a tampered envelope opened without error")
	}
}

// An envelope that is not one of ours — a wrong version, a wrong algorithm, or
// a document that is not an envelope at all — is refused by name rather than
// folded into a decode failure. The box opening an envelope from a future cafe
// has to be able to say "I cannot open this" and mean it.
func TestOpenRefusesAnEnvelopeThatIsNotOurs(t *testing.T) {
	pub, priv := generateBox(t)

	envelope, err := Seal("credential", pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(envelope), &env); err != nil {
		t.Fatalf("the envelope is not JSON: %v", err)
	}

	cases := map[string]string{
		"wrong version": func() string {
			env.V = EnvelopeVersion + 1
			raw, _ := json.Marshal(env)
			return string(raw)
		}(),
		"wrong algorithm": func() string {
			env.Alg = "RSA-OAEP+A256GCM"
			raw, _ := json.Marshal(env)
			return string(raw)
		}(),
		"not json":  "this is not an envelope",
		"empty":     "",
		"truncated": envelope[:len(envelope)/2],
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(doc, priv); err == nil {
				t.Errorf("opened an envelope that is not ours")
			}
		})
	}
}

// Sealing to a key that is not 32 bytes is refused rather than producing an
// envelope that cannot be opened. The moment to find out the key is the wrong
// length is here, not at the far end.
func TestSealRefusesABoxKeyOfTheWrongLength(t *testing.T) {
	short := ed25519.PublicKey("too-short")
	if _, err := Seal("credential", short); err == nil {
		t.Errorf("sealed to a public key of the wrong length")
	}
}

// The identity point has no X25519 form (1-y = 0, no inverse). It is refused
// rather than converted to a key that would produce an unopenable envelope.
func TestSealRefusesTheIdentityPoint(t *testing.T) {
	// The identity point is y == 1, x == 0: a 0x01 in the FIRST byte (y is
	// little-endian) and the sign bit clear.
	identity := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	identity[0] = 0x01
	if _, err := Seal("credential", identity); err == nil {
		t.Errorf("sealed to the identity point")
	}
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
