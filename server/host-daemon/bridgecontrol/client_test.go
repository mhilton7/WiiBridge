package bridgecontrol

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func certificate(t *testing.T, name string) (tlsCertificate []byte, key []byte) {
	return certificateWithValidity(t, name, time.Now().Add(-time.Hour),
		time.Now().Add(time.Hour))
}

func certificateWithValidity(t *testing.T, name string, notBefore, notAfter time.Time) (
	tlsCertificate []byte, key []byte,
) {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)})
}

func TestExpiredPinnedCertificateRemainsAValidDeviceIdentity(t *testing.T) {
	const token = "independent-pi-token"
	certPEM, keyPEM := certificateWithValidity(t, "device",
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	certPath := filepath.Join(t.TempDir(), "expired-device.crt")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	server.TLS = serverTLS(t, certPEM, keyPEM)
	server.StartTLS()
	defer server.Close()

	client, err := New(server.URL, token, certPath)
	if err != nil {
		t.Fatalf("expired exact-pinned certificate was rejected: %v", err)
	}
	if err = client.Action(context.Background(), "detach"); err != nil {
		t.Fatalf("expired exact-pinned certificate failed TLS: %v", err)
	}
}

func TestExpiredCertificateStillRequiresExactPin(t *testing.T) {
	const token = "independent-pi-token"
	serverCert, serverKey := certificateWithValidity(t, "device",
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	pinnedCert, _ := certificateWithValidity(t, "other-device",
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	certPath := filepath.Join(t.TempDir(), "wrong-device.crt")
	if err := os.WriteFile(certPath, pinnedCert, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	server.TLS = serverTLS(t, serverCert, serverKey)
	server.StartTLS()
	defer server.Close()

	client, err := New(server.URL, token, certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Action(context.Background(), "detach"); err == nil ||
		!strings.Contains(err.Error(), "certificate pin mismatch") {
		t.Fatalf("wrong certificate pin error = %v", err)
	}
}

func serverTLS(t *testing.T, certificatePEM, keyPEM []byte) *tls.Config {
	t.Helper()
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
}

func TestActionUsesPinnedCertificateAndPiAuthentication(t *testing.T) {
	const token = "independent-pi-token"
	certPEM, keyPEM := certificate(t, "device")
	certPath := filepath.Join(t.TempDir(), "device.crt")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		expectedCSRF := sha256Hex(csrfPrefix + token)
		if r.URL.Path != "/api/v1/action/detach" || !ok || user != "admin" ||
			password != token || r.Header.Get("X-WiiBridge-CSRF") != expectedCSRF {
			t.Errorf("unexpected authenticated action request: %#v", r)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = serverTLS(t, certPEM, keyPEM)
	server.StartTLS()
	defer server.Close()

	client, err := New(server.URL, token, certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Action(context.Background(), "detach"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Pi action was not called")
	}
}

func TestConfigurationIsOptionalButNeverPartial(t *testing.T) {
	client, err := New("", "", "")
	if err != nil || client != nil {
		t.Fatalf("empty configuration = (%v, %v), want disabled", client, err)
	}
	if _, err = New("https://pi:9443", "", "device.crt"); err == nil {
		t.Fatal("partial automatic switching configuration was accepted")
	}
}

func TestActionRejectsUnlistedHelper(t *testing.T) {
	client := &Client{}
	if err := client.Action(context.Background(), "clear-cache"); err == nil {
		t.Fatal("unsafe helper action was accepted")
	}
}

func TestStatusUsesPinnedAuthenticatedConnection(t *testing.T) {
	const token = "independent-pi-token"
	certPEM, keyPEM := certificate(t, "device")
	certPath := filepath.Join(t.TempDir(), "device.crt")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if r.URL.Path != "/api/v1/status" || !ok || user != "admin" || password != token {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(Status{
			Target: "zero-w-armhf", BoardOK: true, NBDConnected: true,
			USBAttached: true, ExportMode: "wii", State: "ready",
		})
	}))
	server.TLS = serverTLS(t, certPEM, keyPEM)
	server.StartTLS()
	defer server.Close()

	client, err := New(server.URL, token, certPath)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Target != "zero-w-armhf" || status.ExportMode != "wii" ||
		!status.NBDConnected || !status.USBAttached {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
