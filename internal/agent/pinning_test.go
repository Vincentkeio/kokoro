package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

type pinningIdentity struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newPinningIdentity(t *testing.T, serial int64, parent *pinningIdentity, ca bool, change func(*x509.Certificate)) *pinningIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), IsCA: ca, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"panel.example.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	if ca {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	if change != nil {
		change(template)
	}
	signer, signerKey := template, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &pinningIdentity{cert, key}
}

func pinningFingerprint(cert *x509.Certificate) string {
	digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(digest[:])
}

func TestPinnedCertificatesRequireValidChain(t *testing.T) {
	root := newPinningIdentity(t, 1, nil, true, nil)
	intermediate := newPinningIdentity(t, 2, root, true, nil)
	leaf := newPinningIdentity(t, 3, intermediate, false, nil)
	rogue := newPinningIdentity(t, 4, nil, false, nil)
	expired := newPinningIdentity(t, 5, root, false, func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) })
	clientOnly := newPinningIdentity(t, 6, root, false, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} })
	for _, tc := range []struct {
		name    string
		chain   []*x509.Certificate
		pin     *x509.Certificate
		host    string
		allowed bool
	}{
		{"root pin", []*x509.Certificate{leaf.cert, intermediate.cert, root.cert}, root.cert, "panel.example.test", true},
		{"intermediate pin", []*x509.Certificate{leaf.cert, intermediate.cert}, intermediate.cert, "panel.example.test", true},
		{"leaf pin", []*x509.Certificate{leaf.cert}, leaf.cert, "panel.example.test", true},
		{"self-signed leaf", []*x509.Certificate{rogue.cert}, rogue.cert, "panel.example.test", true},
		{"unrelated root", []*x509.Certificate{rogue.cert, root.cert}, root.cert, "panel.example.test", false},
		{"replayed leaf", []*x509.Certificate{rogue.cert, leaf.cert}, leaf.cert, "panel.example.test", false},
		{"missing intermediate", []*x509.Certificate{leaf.cert, root.cert}, root.cert, "panel.example.test", false},
		{"wrong pin", []*x509.Certificate{leaf.cert, intermediate.cert, root.cert}, rogue.cert, "panel.example.test", false},
		{"wrong host", []*x509.Certificate{leaf.cert, intermediate.cert, root.cert}, root.cert, "other.example.test", false},
		{"expired", []*x509.Certificate{expired.cert, root.cert}, root.cert, "panel.example.test", false},
		{"wrong usage", []*x509.Certificate{clientOnly.cert, root.cert}, root.cert, "panel.example.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw [][]byte
			for _, cert := range tc.chain {
				raw = append(raw, cert.Raw)
			}
			err := verifyPinned(raw, pinningFingerprint(tc.pin), tc.host)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
		})
	}
}

func TestPinnedTLSRejectsAttackerBeforeSendingRequest(t *testing.T) {
	root := newPinningIdentity(t, 1, nil, true, nil)
	rogue := newPinningIdentity(t, 2, nil, false, nil)
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{rogue.cert.Raw, root.cert.Raw}, PrivateKey: rogue.key}}}
	server.StartTLS()
	defer server.Close()
	a, err := New(&model.AgentConfig{Hub: server.URL, CAFingerprint: "sha256/" + pinningFingerprint(root.cert)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer nt_test_secret")
	if response, err := a.client.Do(req); err == nil {
		response.Body.Close()
		t.Fatal("unrelated pinned CA authenticated attacker")
	}
	if requests.Load() != 0 {
		t.Fatal("request credentials reached attacker")
	}
}

func TestFingerprintFormatsWithBase64Slashes(t *testing.T) {
	var digest [32]byte
	digest[0] = 255
	want := base64.StdEncoding.EncodeToString(digest[:])
	for _, raw := range []string{want, base64.RawStdEncoding.EncodeToString(digest[:]), base64.URLEncoding.EncodeToString(digest[:]), base64.RawURLEncoding.EncodeToString(digest[:]), hex.EncodeToString(digest[:]), "sha256/" + want, "SHA256/" + want} {
		if got, err := normalizeFingerprint(raw); err != nil || got != want {
			t.Fatalf("fingerprint %q: %q %v", raw, got, err)
		}
	}
	if _, err := normalizeFingerprint("sha512/" + want); err == nil {
		t.Fatal("unknown fingerprint algorithm accepted")
	}
}
