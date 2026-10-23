package edge

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CertDirName is where the box keeps the certificates the edge serves.
//
// All three modes end up as PEM files on disk, and that is the decision worth
// explaining: the edge does not implement an ACME client, because a client
// that talks to Let's Encrypt is a client that needs an account key, a
// nonce, a challenge lifecycle and a rate limit — and the half of automatic
// TLS a customer actually cares about is that the right certificate is served
// for the right name, which is a lookup. The minting is the substrate's job
// (cert-manager already does it on the launch box); the edge's job is to
// serve what was issued and to say honestly when nothing was.
const CertDirName = "certs"

// certPair is one hostname's certificate and key on disk.
func certPair(dir, name string) (certPath, keyPath string) {
	return filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
}

// Certificates serves the box's TLS material. It is the seam the gateway's
// tls.Config points at, and it is an interface for the same reason every seam
// in this repository is: the local proof has to be able to run against a
// certificate minted in memory, and a box has to be able to serve one read
// from disk, without either one being a build tag or an environment check.
type Certificates interface {
	// Certificate returns the certificate to serve for the name in the
	// ClientHello, or nil to let the handshake fail. Returning nil rather
	// than a self-signed fallback is deliberate: a fallback certificate is
	// a certificate a client will reject anyway, served with a name that
	// lies about who it belongs to.
	Certificate(name string) (*tls.Certificate, error)
}

// CertStore reads certificates the box was issued, from the edge's cert
// directory.
//
// It caches by path and modification time, so a certificate that was renewed
// on disk is picked up without the edge restarting — a restart to reload a
// certificate is a restart, and a restart on the edge is a blip on every
// hostname the box serves.
type CertStore struct {
	Dir string

	mu    sync.Mutex
	cache map[string]cachedCert
}

type cachedCert struct {
	cert    *tls.Certificate
	modTime time.Time
}

// NewCertStore is the store for one box's cert directory.
func NewCertStore(dir string) *CertStore {
	return &CertStore{Dir: dir, cache: map[string]cachedCert{}}
}

// Certificate loads, caches and returns one hostname's pair. A missing pair
// is an error that names the name and the directory, because "TLS is broken"
// on a box is a sentence that sends an operator to the wrong place and
// "api.anywaye.test has no certificate in /var/lib/boxd/edge/certs" does not.
func (s *CertStore) Certificate(name string) (*tls.Certificate, error) {
	certPath, keyPath := certPair(s.Dir, name)
	certStat, err := os.Stat(certPath)
	if err != nil {
		return nil, fmt.Errorf("the certificate for %s: %w", name, err)
	}
	keyStat, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("the key for %s: %w", name, err)
	}
	mod := certStat.ModTime()
	if keyStat.ModTime().After(mod) {
		mod = keyStat.ModTime()
	}

	s.mu.Lock()
	if c, ok := s.cache[name]; ok && !c.modTime.Before(mod) {
		s.mu.Unlock()
		return c.cert, nil
	}
	s.mu.Unlock()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load the certificate for %s: %w", name, err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("the certificate for %s is empty", name)
	}
	s.mu.Lock()
	s.cache[name] = cachedCert{cert: &cert, modTime: mod}
	s.mu.Unlock()
	return &cert, nil
}

// Minted serves certificates the box issued itself, for names that are never
// public. It is the ModeInternal answer and it is also what the local proof
// runs against, and both of those are the same thing: a box that cannot get a
// publicly-trusted certificate for a name that is not public should still put
// encryption on the wire, and it should not need a Let's Encrypt account to
// do it.
type Minted struct {
	// Names is the set of hostnames one certificate covers. The certificate
	// is minted once and served for all of them, because a `.svc` zone on
	// one box is one certificate with one expiry rather than one per
	// service.
	Names []string

	mu     sync.Mutex
	issued *tls.Certificate
}

// NewMinted is a minted source for the given hostnames.
func NewMinted(names ...string) *Minted { return &Minted{Names: names} }

// Certificate mints on first use and returns the same certificate afterwards.
// Minting is lazy because a gateway that never receives a TLS handshake
// should not have spent a key generation on one.
func (m *Minted) Certificate(name string) (*tls.Certificate, error) {
	if !m.covers(name) {
		return nil, fmt.Errorf("the box issued no internal certificate for %s", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.issued != nil {
		return m.issued, nil
	}
	cert, err := mintCert(m.Names)
	if err != nil {
		return nil, err
	}
	m.issued = cert
	return m.issued, nil
}

// covers reports whether the name is one this source was minted for. A
// wildcard entry is supported because the `.svc` zone is exactly that shape —
// `*.svc.anywaye.com` is the plan's internal zone (stability rule 4: it never
// appears in public DNS) and a source that had to name each service would be
// a list that goes stale the moment a service is added.
func (m *Minted) covers(name string) bool {
	name = NormalizeHost(name)
	for _, n := range m.Names {
		n = NormalizeHost(n)
		if n == name {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			suffix := strings.TrimPrefix(n, "*")
			if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
				return true
			}
		}
	}
	return false
}

// mintCert issues a self-signed certificate for the given names. It is one
// call of the standard library and it is here rather than in a helper package
// because the edge is the only thing in this repository that needs to put a
// certificate on the wire without asking an authority for one.
//
// THE PRIVATE KEY NEVER LEAVES THIS FUNCTION. It is carried inside the
// *tls.Certificate and nowhere else: no field on any type the edge returns,
// no log line, no JSON document. The same rule internal/boxd's Identity holds
// — the key is not a thing this package has a way to hand out.
func mintCert(names []string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate the serial: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0], Organization: []string{"anywaye box"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(strings.TrimSuffix(n, ".")); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, NormalizeHost(n))
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sign the certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse the certificate: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// ACMEChallengeDirName is the directory HTTP-01 tokens are read from.
//
// The edge serves the challenge and the substrate performs it: a token
// appears here (cert-manager writes it on the launch box) and the edge answers
// `/.well-known/acme-challenge/<token>` with its contents. Splitting it this
// way is what keeps the ACME client out of this package — see CertDirName's
// comment — without making the edge unable to be issued for.
const ACMEChallengeDirName = "acme"

// ChallengePathPrefix is the path Let's Encrypt's HTTP-01 solver asks for.
const ChallengePathPrefix = "/.well-known/acme-challenge/"

// ChallengeHandler serves HTTP-01 tokens from the edge's own directory. It is
// registered on the plaintext listener a box also runs on :80, because a
// challenge that only answered over TLS would be a challenge the CA could
// not complete.
//
// A token that is not there is a 404 from this handler and not a proxy pass
// to some host's upstream: the path is the edge's, and a request for it is
// never a request for an app.
func ChallengeHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, ChallengePathPrefix)
		// A token with a separator in it is not a token, and answering one
		// would be answering a path traversal attempt.
		if token == "" || strings.ContainsAny(token, `/\`) || token == "." || token == ".." {
			http.Error(w, "no such challenge token", http.StatusNotFound)
			return
		}
		body, err := os.ReadFile(filepath.Join(dir, token))
		if err != nil {
			http.Error(w, "no such challenge token", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}

// ErrNoCertificate is what Certificate returns when nothing was issued for a
// name and nothing was minted. It is a sentinel so a caller can tell "no
// certificate yet" from "the certificate is broken", which are different
// situations: the first is a deploy that has not finished issuing, the second
// is an operator's file to fix.
var ErrNoCertificate = errors.New("no certificate for this name")
