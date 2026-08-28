// Package bridgecontrol coordinates the small, fixed set of privileged
// transitions exposed by a WiiBridge Raspberry Pi controller.
package bridgecontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"wiibridge/shared/compat"
	"wiibridge/shared/perf"
)

const csrfPrefix = "wiibridge-setup-csrf\x00"
const maxStatusResponse = 64 << 10

var allowedActions = map[string]bool{
	"detach": true, "disconnect": true, "connect-wii": true,
	"connect-gamecube-physical": true, "connect-gamecube-emulated": true,
	"attach": true, "poweroff": true, "reboot": true,
}

// Client authenticates the Pi by an exact certificate pin and authenticates
// each request with the Pi's independently provisioned management token. The
// exact DER pin, rather than the certificate's validity dates, defines the
// lifetime of this appliance identity.
type Client struct {
	baseURL string
	token   string
	csrf    string
	http    *http.Client
}

// Status is the live, non-secret operational state returned by the Pi
// controller. It intentionally mirrors only the controller's public status
// document and never exposes provisioning values or credentials.
type Status struct {
	Target        string            `json:"target"`
	Board         string            `json:"detected_board"`
	BoardOK       bool              `json:"board_compatible"`
	Provisioned   bool              `json:"provisioned"`
	WiFiReady     bool              `json:"wifi_provisioned"`
	AutoAttach    bool              `json:"auto_attach"`
	NBDConnected  bool              `json:"nbd_connected"`
	USBAttached   bool              `json:"usb_attached"`
	ExportMode    string            `json:"export_mode"`
	USBController string            `json:"usb_controller"`
	USBState      string            `json:"usb_state"`
	Addresses     []string          `json:"addresses"`
	State         string            `json:"state"`
	Compatibility compat.Descriptor `json:"compatibility"`
}

func New(baseURL, token, certificatePath string) (*Client, error) {
	if baseURL == "" && token == "" && certificatePath == "" {
		return nil, nil
	}
	if baseURL == "" || token == "" || certificatePath == "" {
		return nil, errors.New("automatic Pi switching requires URL, token, and pinned certificate")
	}
	if len(token) < 12 {
		return nil, errors.New("Pi management token is too short")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Pi management URL must be a plain https URL")
	}
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		return nil, fmt.Errorf("read Pi certificate: %w", err)
	}
	block, _ := decodeCertificate(certificatePEM)
	if block == nil {
		return nil, errors.New("Pi certificate contains no X.509 certificate")
	}
	certificate, err := x509.ParseCertificate(block)
	if err != nil {
		return nil, fmt.Errorf("parse Pi certificate: %w", err)
	}
	pin := sha256.Sum256(certificate.Raw)
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Hostname verification cannot be used with the Pi's device-unique,
		// self-signed setup certificate. VerifyPeerCertificate below performs
		// exact certificate pinning instead.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) != 1 {
				return errors.New("Pi returned an unexpected certificate chain")
			}
			got := sha256.Sum256(rawCerts[0])
			if subtle.ConstantTimeCompare(got[:], pin[:]) != 1 {
				return errors.New("Pi certificate pin mismatch")
			}
			return nil
		},
	}
	csrf := sha256.Sum256([]byte(csrfPrefix + token))
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		csrf:    hex.EncodeToString(csrf[:]),
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("Pi controller redirects are forbidden")
			},
		},
	}, nil
}

func (c *Client) Action(ctx context.Context, action string) error {
	if c == nil {
		return errors.New("Pi switching is not configured")
	}
	if !allowedActions[action] {
		return errors.New("unsupported Pi action")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/action/"+action, nil)
	if err != nil {
		return err
	}
	request.SetBasicAuth("admin", c.token)
	request.Header.Set("X-WiiBridge-CSRF", c.csrf)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Pi %s request: %w", action, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Pi %s returned HTTP %d", action, response.StatusCode)
	}
	return nil
}

// Status retrieves a fresh controller status using the same certificate pin
// and independent Pi credentials as privileged actions.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if c == nil {
		return status, errors.New("Pi switching is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/status", nil)
	if err != nil {
		return status, err
	}
	request.SetBasicAuth("admin", c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return status, fmt.Errorf("Pi status request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return status, fmt.Errorf("Pi status returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxStatusResponse))
	if err := decoder.Decode(&status); err != nil {
		return status, fmt.Errorf("decode Pi status: %w", err)
	}
	return status, nil
}

func (c *Client) Metrics(ctx context.Context) (perf.PiSnapshot, error) {
	var metrics perf.PiSnapshot
	if c == nil {
		return metrics, errors.New("Pi switching is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/metrics", nil)
	if err != nil {
		return metrics, err
	}
	request.SetBasicAuth("admin", c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return metrics, fmt.Errorf("Pi metrics request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return metrics, fmt.Errorf("Pi metrics returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxStatusResponse))
	if err = decoder.Decode(&metrics); err != nil {
		return metrics, fmt.Errorf("decode Pi metrics: %w", err)
	}
	return metrics, nil
}
