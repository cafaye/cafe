package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// The envelope version and algorithm this package writes. They are in the
// document rather than in a comment because the box, opening the envelope
// later, has to branch on them: an envelope from a future cafe that speaks a
// different algorithm is a box's honest "I cannot open this" rather than a
// silent decode.
const (
	EnvelopeVersion = 1
	// EnvelopeAlgorithm is the hybrid scheme: an ephemeral X25519 keypair
	// agrees an AES-256-GCM key with the box's key via ECDH, and the
	// credential rides inside the AEAD. It is named to sit beside conductor's
	// own "RSA-OAEP+A256GCM" so a column holding either reads as one family.
	EnvelopeAlgorithm = "X25519+A256GCM"
)

// Envelope is the sealed form of one credential. It is the whole of what
// conductor stores in `sealed_credential` and the whole of what crosses the
// wire: five fields, every one but v/alg base64, and not one of them the
// credential in any recoverable form — `data` is AES-256-GCM ciphertext under
// a key that is not in the document, and `key` is an ephemeral public key
// whose private half was discarded the moment the envelope was sealed.
//
// `key` is the ephemeral X25519 public key, NOT a wrapped data key. ECDH
// derives the shared secret directly from (ephemeral private, box public), so
// there is nothing to wrap the way conductor's RSA-OAEP wraps a random AES
// key. The box recomputes the same secret from (box private, this ephemeral
// public) and opens the AEAD.
type Envelope struct {
	V    int    `json:"v"`
	Alg  string `json:"alg"`
	Key  string `json:"key"`
	IV   string `json:"iv"`
	Tag  string `json:"tag"`
	Data string `json:"data"`
}

// ErrUnknownEnvelope is what Open answers for a document that is not one of
// ours — a wrong version, a wrong algorithm, or a field that will not decode.
// It is a sentinel so a caller can say "this envelope is not mine" rather than
// reporting a decode failure that names no cause.
var ErrUnknownEnvelope = fmt.Errorf("envelope is not a cafe X25519+A256GCM envelope")

// field order, so a sealed envelope's bytes are stable across runs of one
// input. Conductor's SealedEnvelope has the same property and for the same
// reason: a column that can hold two spellings of the same envelope is a
// column a diff cannot read.
var envelopeFields = []string{"v", "alg", "key", "iv", "tag", "data"}

// ed25519P is the field prime, 2^255 - 19. It is named rather than inlined
// because the conversion below is two modular operations over it and a reader
// who has to check them should not have to check whether the constant is
// right first.
var ed25519P, _ = new(big.Int).SetString("57896044618658097711785492504343953926634992332820282019728792003956564819949", 10)

// ed25519PublicToX25519 converts an ed25519 public key to the X25519 public
// key of the same keypair, by the birational map between Edwards25519 and
// Curve25519: u = (1+y)/(1-y), where y is the Edwards y-coordinate the
// ed25519 public key encodes.
//
// This is the conversion NaCl's crypto_sign_ed25519_pk_to_curve25519 performs,
// and it is what makes the box's ONE keypair usable for both signing and
// receiving sealed credentials: the ed25519 public key in box.json is not
// itself an X25519 key, but it converts to the one that is.
//
// The sign bit ed25519 stores in the top bit is the sign of x, which u does
// not depend on, so it is masked off before y is read. The map is undefined
// for y == 1 (the identity point); that key is refused rather than converted,
// because an envelope sealed to it would be an envelope nobody can open.
func ed25519PublicToX25519(ed25519Public ed25519.PublicKey) (ed25519.PublicKey, error) {
	if len(ed25519Public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key is %d bytes, want %d", len(ed25519Public), ed25519.PublicKeySize)
	}
	// y is the little-endian integer the 32 bytes encode, with the sign bit
	// (bit 255) cleared so only y remains.
	yBytes := make([]byte, ed25519.PublicKeySize)
	copy(yBytes, ed25519Public)
	yBytes[ed25519.PublicKeySize-1] &= 0x7f
	reverseBytes(yBytes) // big.Int reads big-endian
	y := new(big.Int).SetBytes(yBytes)

	num := new(big.Int).Add(big.NewInt(1), y)
	den := new(big.Int).Sub(big.NewInt(1), y)
	den.Mod(den, ed25519P)
	inv := new(big.Int).ModInverse(den, ed25519P)
	if inv == nil {
		return nil, fmt.Errorf("ed25519 public key is the identity point; it has no X25519 form")
	}
	u := new(big.Int).Mul(num, inv)
	u.Mod(u, ed25519P)

	out := make([]byte, ed25519.PublicKeySize)
	u.FillBytes(out)  // big-endian
	reverseBytes(out) // X25519, like ed25519, is little-endian
	return ed25519.PublicKey(out), nil
}

// ed25519PrivateToX25519 converts an ed25519 private key to the X25519 private
// key of the same keypair. The ed25519 form is the 64-byte seed-and-public
// pair cafe stores in the keyring; the X25519 form is the clamp of the first
// half of SHA-512 of the seed, which is the same derivation ed25519 itself
// uses before it signs. The public half of the input is ignored, so a caller
// that has only the seed loses nothing.
func ed25519PrivateToX25519(ed25519Private ed25519.PrivateKey) ([]byte, error) {
	if len(ed25519Private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 private key is %d bytes, want %d", len(ed25519Private), ed25519.PrivateKeySize)
	}
	h := sha512.Sum512(ed25519Private[:32]) // the seed is the first half
	k := make([]byte, 32)
	copy(k, h[:32])
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k, nil
}

// reverseBytes reverses a slice in place. ed25519 and X25519 both encode
// scalars little-endian and math/big reads big-endian, so every crossing of
// that boundary goes through this.
func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// Seal encrypts credential to the box's ed25519 public key and returns the
// envelope as a JSON string — the exact bytes conductor stores and the exact
// bytes that cross the wire.
//
// The credential is in plaintext for the duration of this call and nowhere
// else: it is passed to the AEAD as the plaintext argument and is not copied
// into the envelope, the logs or the return value in any recoverable form. The
// caller's responsibility begins the moment the call returns — a credential a
// caller keeps is a credential that ends up in a transcript.
func Seal(credential string, boxPublic ed25519.PublicKey) (string, error) {
	x25519Public, err := ed25519PublicToX25519(boxPublic)
	if err != nil {
		return "", err
	}

	// Ephemeral keypair: the private half is discarded when this function
	// returns, so the shared secret cannot be recomputed from the envelope
	// alone — only the box, holding the private half of the LONG-TERM key,
	// can recompute it.
	ephemeralPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ephemeral X25519 key: %w", err)
	}
	ephemeralPublic := ephemeralPrivate.PublicKey()

	boxX25519Public, err := ecdh.X25519().NewPublicKey(x25519Public)
	if err != nil {
		return "", fmt.Errorf("box public key is not a usable X25519 key: %w", err)
	}
	shared, err := ephemeralPrivate.ECDH(boxX25519Public)
	if err != nil {
		return "", fmt.Errorf("derive the shared secret: %w", err)
	}

	data, iv, tag, err := sealAESGCM(shared, []byte(credential))
	if err != nil {
		return "", err
	}

	env := Envelope{
		V:    EnvelopeVersion,
		Alg:  EnvelopeAlgorithm,
		Key:  base64.StdEncoding.EncodeToString(ephemeralPublic.Bytes()),
		IV:   base64.StdEncoding.EncodeToString(iv),
		Tag:  base64.StdEncoding.EncodeToString(tag),
		Data: base64.StdEncoding.EncodeToString(data),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("encode the envelope: %w", err)
	}
	return string(raw), nil
}

// Open recovers the credential from an envelope, using the box's ed25519
// private key. It is the inverse of Seal and it is here for the round-trip
// tests and for the box side (boxd, W2-P7) when it opens what conductor stored.
//
// Conductor never calls this — it holds no private key. Its existence is the
// proof that the envelope is a real envelope and not a one-way hash: a
// credential that cannot be opened by the box is a credential that was never
// really handed over.
func Open(envelopeJSON string, boxPrivate ed25519.PrivateKey) (string, error) {
	var env Envelope
	if err := json.Unmarshal([]byte(envelopeJSON), &env); err != nil {
		return "", fmt.Errorf("%w: it is not JSON: %v", ErrUnknownEnvelope, err)
	}
	if env.V != EnvelopeVersion {
		return "", fmt.Errorf("%w: version %d, want %d", ErrUnknownEnvelope, env.V, EnvelopeVersion)
	}
	if env.Alg != EnvelopeAlgorithm {
		return "", fmt.Errorf("%w: algorithm %q, want %q", ErrUnknownEnvelope, env.Alg, EnvelopeAlgorithm)
	}

	ephemeralPublic, err := base64.StdEncoding.DecodeString(env.Key)
	if err != nil {
		return "", fmt.Errorf("%w: key is not base64", ErrUnknownEnvelope)
	}
	iv, err := base64.StdEncoding.DecodeString(env.IV)
	if err != nil {
		return "", fmt.Errorf("%w: iv is not base64", ErrUnknownEnvelope)
	}
	tag, err := base64.StdEncoding.DecodeString(env.Tag)
	if err != nil {
		return "", fmt.Errorf("%w: tag is not base64", ErrUnknownEnvelope)
	}
	data, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return "", fmt.Errorf("%w: data is not base64", ErrUnknownEnvelope)
	}

	x25519Private, err := ed25519PrivateToX25519(boxPrivate)
	if err != nil {
		return "", err
	}
	private, err := ecdh.X25519().NewPrivateKey(x25519Private)
	if err != nil {
		return "", fmt.Errorf("box private key is not a usable X25519 key: %w", err)
	}
	public, err := ecdh.X25519().NewPublicKey(ephemeralPublic)
	if err != nil {
		return "", fmt.Errorf("%w: the ephemeral key is not a usable X25519 key", ErrUnknownEnvelope)
	}
	shared, err := private.ECDH(public)
	if err != nil {
		return "", fmt.Errorf("derive the shared secret: %w", err)
	}

	plaintext, err := openAESGCM(shared, data, iv, tag)
	if err != nil {
		// A GCM auth failure is an envelope that was tampered with or sealed to
		// a different box. Both are "this is not for you", and the AEAD's
		// refusal is the security boundary — the message must not carry a byte
		// of the plaintext, not even on failure.
		return "", fmt.Errorf("open the envelope: %w", err)
	}
	return string(plaintext), nil
}

// sealAESGCM is the data plane: AES-256-GCM under the shared secret. The
// overhead (auth tag) is split out of the ciphertext so the envelope's fields
// match conductor's shape — {iv, tag, data} — and a box written against
// conductor's own envelope reads this one without a second idea of the layout.
func sealAESGCM(key, plaintext []byte) (data, iv, tag []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the shared secret is not an AES-256 key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("AES-256-GCM is unavailable: %w", err)
	}
	iv = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, nil, nil, fmt.Errorf("draw the GCM nonce: %w", err)
	}
	sealed := gcm.Seal(nil, iv, plaintext, nil) // sealed = ciphertext || tag
	cut := len(sealed) - gcm.Overhead()
	return sealed[:cut], iv, sealed[cut:], nil
}

// openAESGCM is the inverse of sealAESGCM. It refuses rather than decrypts on
// any authentication failure, which is the property that makes an envelope
// tamper-evident: a flipped bit in `data` fails here instead of producing a
// different plaintext.
func openAESGCM(key, data, iv, tag []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := make([]byte, 0, len(data)+len(tag))
	sealed = append(sealed, data...)
	sealed = append(sealed, tag...)
	return gcm.Open(nil, iv, sealed, nil)
}
