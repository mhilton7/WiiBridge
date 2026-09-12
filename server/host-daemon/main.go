package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	webauth "wiibridge/server/host-daemon/auth"
	"wiibridge/server/host-daemon/bridgecontrol"
	"wiibridge/server/host-daemon/exportprofile"
	"wiibridge/server/host-daemon/gamecube"
	"wiibridge/server/host-daemon/scanner"
	"wiibridge/server/host-daemon/store"
	"wiibridge/server/host-daemon/vdisk"
	webui "wiibridge/server/host-daemon/web"
	"wiibridge/server/nbd-plugin"
	"wiibridge/shared/compat"
	"wiibridge/shared/model"
	"wiibridge/shared/perf"
	"wiibridge/shared/sourcehealth"
)

const version = "0.1.0-rc.1"

var (
	gitCommit  = "unknown"
	buildTime  = "unknown"
	buildDirty = "unknown"
)

func buildVersion() string {
	return fmt.Sprintf("WiiBridge %s\ncommit %s\nbuilt %s\ndirty %s\ngo %s\ntarget %s/%s",
		version, gitCommit, buildTime, buildDirty, runtime.Version(),
		runtime.GOOS, runtime.GOARCH)
}

func displayRevision(revision string) string {
	const displayLength = 12
	if len(revision) <= displayLength {
		return revision
	}
	return revision[:displayLength]
}

type app struct {
	mu                   sync.RWMutex
	switchMu             sync.Mutex
	scanMu               sync.Mutex
	root                 string
	gcRoot               string
	dataDir              string
	disk                 *vdisk.Disk
	scan                 scanner.Result
	tokenSum             [32]byte
	started              time.Time
	store                *store.Store
	gcScan               gamecube.Result
	exports              *exportprofile.Manager
	wii                  *wiiExportProfile
	csrf                 string
	imports              map[string]importJob
	activeGC             *gamecube.VolumeManifest
	activeGCLibrary      *gamecube.LibraryManifest
	gcLibrary            *gamecube.LibraryManager
	gcMode               gamecube.MemoryCardMode
	gcUpdate             bool
	ready                bool
	gcStartupPhase       string
	gcStartupError       string
	browser              *webauth.Manager
	web                  *webui.Renderer
	authMu               sync.Mutex
	failures             map[string]authFailure
	pi                   piController
	gcSaves              *gamecube.SaveStore
	gcSaveSelection      gamecube.SaveSelection
	gcSaveError          string
	metricsRegistry      *perf.Registry
	source               sourcehealth.Record
	gcSource             sourcehealth.Record
	hostDescriptor       compat.Descriptor
	compatibility        compat.Result
	maxSessions          int
	sessionRetentionDays int
	memoryLimit          int64
	dashboardRefresh     time.Duration
	metricsPersistence   time.Duration
	sourceFailures       chan string
	lastPiUSBResets      uint64
}

type piController interface {
	Action(context.Context, string) error
	Probe(context.Context) (bridgecontrol.Status, error)
	Status(context.Context) (bridgecontrol.Status, error)
	Address() string
	SetAddress(context.Context, string) error
}

type authFailure struct {
	count int
	reset time.Time
}

type importJob struct {
	Status   string                   `json:"status"`
	Error    string                   `json:"error,omitempty"`
	Manifest *gamecube.VolumeManifest `json:"manifest,omitempty"`
}

type startupHandler struct {
	mu           sync.RWMutex
	phase        string
	lastComplete string
	failure      string
	started      time.Time
	phaseStarted time.Time
}

func (h *startupHandler) SetPhase(phase string) {
	now := time.Now()
	h.mu.Lock()
	previous := h.phase
	previousStarted := h.phaseStarted
	if h.started.IsZero() {
		h.started = now
	}
	if previous != "" {
		h.lastComplete = previous
	}
	h.phase = phase
	h.failure = ""
	h.phaseStarted = now
	started := h.started
	h.mu.Unlock()
	if previous == "" {
		slog.Info("Host startup phase started", "phase", phase)
		return
	}
	slog.Info("Host startup phase completed",
		"phase", previous, "duration", now.Sub(previousStarted),
		"next_phase", phase, "total_elapsed", now.Sub(started))
}

func (h *startupHandler) Fail(summary string) {
	now := time.Now()
	h.mu.Lock()
	if h.started.IsZero() {
		h.started = now
	}
	phase := h.phase
	phaseStarted := h.phaseStarted
	h.phase = "Startup failed"
	h.failure = summary
	h.phaseStarted = now
	started := h.started
	h.mu.Unlock()
	slog.Error("Host startup failed", "phase", phase,
		"phase_duration", now.Sub(phaseStarted), "total_elapsed", now.Sub(started),
		"summary", summary)
}

func (h *startupHandler) LogHeartbeat(ctx context.Context, done <-chan struct{},
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case now := <-ticker.C:
			h.mu.RLock()
			phase := h.phase
			started := h.started
			phaseStarted := h.phaseStarted
			h.mu.RUnlock()
			slog.Info("Host startup still running", "phase", phase,
				"phase_elapsed", now.Sub(phaseStarted),
				"total_elapsed", now.Sub(started))
		}
	}
}

func (h *startupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	phase := h.phase
	lastComplete := h.lastComplete
	failure := h.failure
	phaseStarted := h.phaseStarted
	h.mu.RUnlock()
	status := "starting"
	if failure != "" {
		status = "failed"
	}
	if r.URL.Path == "/healthz" {
		writeJSON(w, map[string]any{
			"status": status, "phase": phase,
			"phase_started":   phaseStarted.UTC().Format(time.RFC3339),
			"elapsed_seconds": int(time.Since(phaseStarted).Seconds()),
		})
		return
	}
	if r.URL.Path == "/readyz" {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]any{
				"status": "not-ready", "phase": phase,
				"phase_started":   phaseStarted.UTC().Format(time.RFC3339),
				"elapsed_seconds": int(time.Since(phaseStarted).Seconds()),
			})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	heading := "WiiBridge is starting"
	if failure != "" {
		heading = "WiiBridge startup failed"
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta http-equiv="refresh" content="5">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WiiBridge starting</title></head>
<body><main><h1>%s</h1><p>Current phase: %s</p>
<p>Phase started: %s (%d seconds ago)</p>
<p>Last completed phase: %s</p><p>%s</p>
<p>This page refreshes automatically. Wii and GameCube exports remain unavailable until scanning finishes.</p>
</main></body></html>`,
		html.EscapeString(heading),
		html.EscapeString(phase),
		html.EscapeString(phaseStarted.UTC().Format(time.RFC3339)),
		int(time.Since(phaseStarted).Seconds()),
		html.EscapeString(lastComplete),
		html.EscapeString(failure))
}

type switchingHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func (h *switchingHandler) Set(handler http.Handler) {
	h.mu.Lock()
	h.handler = handler
	h.mu.Unlock()
}

func (h *switchingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	handler := h.handler
	h.mu.RUnlock()
	handler.ServeHTTP(w, r)
}

type wiiExportProfile struct{ app *app }

func (p *wiiExportProfile) Platform() string { return "wii" }
func (p *wiiExportProfile) Backend() nbd.Backend {
	p.app.mu.RLock()
	defer p.app.mu.RUnlock()
	return p.app.disk
}
func (p *wiiExportProfile) ReadOnly() bool { return true }
func (p *wiiExportProfile) Validate() error {
	if p.Backend() == nil {
		return errors.New("Wii export is unavailable")
	}
	return nil
}
func (p *wiiExportProfile) Close() error { return nil }

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "scan":
			scanCLI(os.Args[2:])
			return
		case "healthcheck":
			healthCLI(os.Args[2:])
			return
		case "version":
			fmt.Println(buildVersion())
			return
		}
	}
	if err := serve(); err != nil {
		slog.Error("host stopped", "error", err)
		os.Exit(1)
	}
}

func scanCLI(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	root := fs.String("library", "/library", "read-only WBFS library")
	_ = fs.Parse(args)
	result, err := scanner.Scan(*root)
	if err != nil {
		slog.Error("scan failed", "error", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func healthCLI(args []string) {
	if err := runHealthCheck(args); err != nil {
		slog.Error("health check failed", "error", err)
		os.Exit(1)
	}
}

func runHealthCheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", "https://127.0.0.1:8445/healthz", "health URL")
	caPath := fs.String("ca", "/certs/ca.crt", "CA certificate")
	_ = fs.Parse(args)
	pem, err := os.ReadFile(*caPath)
	if err != nil {
		return fmt.Errorf("read CA %s: %w", *caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("parse CA %s", *caPath)
	}
	// The check runs inside the same container over loopback. Verify the
	// complete server-auth chain, but do not require legacy deployments to
	// reissue an otherwise valid certificate solely to add a 127.0.0.1 SAN.
	tlsConfig := &tls.Config{
		RootCAs: pool, MinVersion: tls.VersionTLS13,
		InsecureSkipVerify: true, // Verification is performed below without DNSName.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("health endpoint presented no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}
			_, verifyErr := state.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: pool, Intermediates: intermediates,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return verifyErr
		},
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: tlsConfig,
	}}
	resp, err := client.Get(*url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", *url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %s", *url, resp.Status)
	}
	return nil
}

func serve() error {
	hostStarted := time.Now()
	slog.Info("Host process entered main",
		"event", "startup", "phase", "process entered main", "elapsed_ms", 0,
		"version", version, "revision", gitCommit, "built", buildTime,
		"dirty", buildDirty, "go_version", runtime.Version(),
		"target", runtime.GOOS+"/"+runtime.GOARCH)
	root, gcRoot, pathErr := configuredLibraryRoots()
	if pathErr != nil {
		return pathErr
	}
	dataDir := env("WIIBRIDGE_DATA", "/data")
	token := os.Getenv("WIIBRIDGE_ADMIN_TOKEN")
	if len(token) < 20 {
		return errors.New("WIIBRIDGE_ADMIN_TOKEN must contain at least 20 characters")
	}
	slog.Info("Host configuration validated",
		"event", "startup", "phase", "configuration validation",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	for platform, sourceRoot := range map[string]string{"wii": root, "gamecube": gcRoot} {
		if err := assertReadOnly(sourceRoot); err != nil {
			if !isUnavailableLibraryError(err) {
				return fmt.Errorf("%s library safety check: %w", platform, err)
			}
			slog.Warn("Library source is unavailable; startup will preserve its catalog", "platform", platform, "code", "SOURCE-OFFLINE")
		}
	}
	dataStarted := time.Now()
	if err := os.MkdirAll(filepath.Join(dataDir, "snapshots"), 0o700); err != nil {
		return err
	}
	slog.Info("Data directory initialization complete",
		"event", "startup", "phase", "data-directory initialization",
		"phase_elapsed_ms", time.Since(dataStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	tlsStarted := time.Now()
	tlsConfig, err := mutualTLSConfig(
		env("WIIBRIDGE_TLS_CERT", "/certs/server.crt"),
		env("WIIBRIDGE_TLS_KEY", "/certs/server.key"),
		env("WIIBRIDGE_TLS_CLIENT_CA", "/certs/clients-ca.crt"),
	)
	if err != nil {
		return err
	}
	slog.Info("TLS certificates loaded",
		"event", "startup", "phase", "TLS certificate loading",
		"phase_elapsed_ms", time.Since(tlsStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	startup := &startupHandler{}
	startup.SetPhase("Initializing configuration")
	startupDone := make(chan struct{})
	go startup.LogHeartbeat(ctx, startupDone, 30*time.Second)
	handler := &switchingHandler{handler: startup}
	errs := make(chan error, 2)
	web := &http.Server{
		Addr:    env("WIIBRIDGE_HTTPS_LISTEN", ":8445"),
		Handler: securityHeaders(handler), TLSConfig: tlsConfig.Clone(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}
	web.TLSConfig.ClientAuth = tls.NoClientCert
	web.TLSConfig.MinVersion = tls.VersionTLS13
	web.TLSConfig.MaxVersion = 0
	webListener, err := net.Listen("tcp", web.Addr)
	if err != nil {
		return err
	}
	slog.Info("Host HTTPS startup listener ready", "address", web.Addr,
		"event", "startup", "phase", "HTTPS listener opened",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	go func() {
		errs <- web.ServeTLS(webListener,
			env("WIIBRIDGE_TLS_CERT", "/certs/server.crt"),
			env("WIIBRIDGE_TLS_KEY", "/certs/server.key"))
	}()
	failStartup := func(summary string, cause error) error {
		startup.Fail(summary)
		select {
		case <-ctx.Done():
		case listenerErr := <-errs:
			if listenerErr != nil && !errors.Is(listenerErr, http.ErrServerClosed) {
				cause = fmt.Errorf("%w (startup HTTPS listener: %v)", cause, listenerErr)
			}
		}
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = web.Shutdown(shutdown)
		return cause
	}

	startup.SetPhase("Opening persistent state")
	gcConfig := gamecube.DefaultLibraryConfig()
	gcConfig.SourceRoot = gcRoot
	gcConfig.HeadroomPercent, err = intEnv("WIIBRIDGE_GAMECUBE_HEADROOM_PERCENT", 5)
	if err != nil {
		return failStartup("GameCube headroom configuration is invalid", err)
	}
	gcConfig.SaveReserveMiB, err = int64Env("WIIBRIDGE_GAMECUBE_SAVE_RESERVE_MIB", 1024)
	if err != nil {
		return failStartup("GameCube save-reserve configuration is invalid", err)
	}
	gcConfig.MaxVolumeGiB, err = int64Env("WIIBRIDGE_GAMECUBE_MAX_VOLUME_GIB", 0)
	if err != nil {
		return failStartup("GameCube volume configuration is invalid", err)
	}
	gcConfig.Retention, err = intEnv("WIIBRIDGE_GAMECUBE_GENERATION_RETENTION", 2)
	if err != nil {
		return failStartup("GameCube retention configuration is invalid", err)
	}
	gcConfig.Mode = gamecube.MemoryCardMode(
		env("WIIBRIDGE_GAMECUBE_MEMORY_CARD_MODE", string(gamecube.MemoryCardPhysical)))
	gcConfig.SavesRoot = filepath.Join(dataDir, "gamecube", "saves")
	gcConfig.CardSize, err = int64Env(
		"WIIBRIDGE_GAMECUBE_CARD_SIZE_BYTES", gamecube.DefaultLibraryCardSize)
	if err != nil {
		return failStartup("GameCube card-size configuration is invalid", err)
	}
	gcConfig.AutoCreateCards, err = boolEnv("WIIBRIDGE_GAMECUBE_AUTO_CREATE_CARDS", true)
	if err != nil {
		return failStartup("GameCube card-creation configuration is invalid", err)
	}
	gcConfig.SharedCardName = env("WIIBRIDGE_GAMECUBE_SHARED_CARD", "shared")
	gcConfig.MaxSaveBackups, err = intEnv(
		"WIIBRIDGE_GAMECUBE_MAX_SAVE_BACKUPS", gamecube.DefaultSaveBackupRetention)
	if err != nil {
		return failStartup("GameCube save-retention configuration is invalid", err)
	}
	gcConfig.Application = version
	autoBackupInterval, err := time.ParseDuration(
		env("WIIBRIDGE_GAMECUBE_AUTO_BACKUP_INTERVAL", "0s"))
	if err != nil || autoBackupInterval < 0 {
		return failStartup("GameCube automatic backup configuration is invalid",
			errors.New("WIIBRIDGE_GAMECUBE_AUTO_BACKUP_INTERVAL must be a non-negative duration"))
	}
	saveSelection := gamecube.SaveSelection{
		FormatVersion: gamecube.SaveOverlayFormatVersion, Mode: gcConfig.Mode,
		CardSize: gcConfig.CardSize, SharedCardName: gcConfig.SharedCardName,
		AutomaticCreation:       gcConfig.AutoCreateCards,
		MaximumRetainedBackups:  gcConfig.MaxSaveBackups,
		AutomaticBackupInterval: int64(autoBackupInterval / time.Second),
	}
	saveSelectionPath := filepath.Join(dataDir, "gamecube", "save-settings.json")
	loadedSelection, saveSelectionErr := gamecube.LoadSaveSelection(
		saveSelectionPath, saveSelection)
	if saveSelectionErr == nil {
		saveSelection = loadedSelection
		gcConfig.Mode, gcConfig.CardSize = saveSelection.Mode, saveSelection.CardSize
		gcConfig.SharedCardName = saveSelection.SharedCardName
		gcConfig.AutoCreateCards = saveSelection.AutomaticCreation
		gcConfig.MaxSaveBackups = saveSelection.MaximumRetainedBackups
	} else {
		slog.Error("Managed GameCube save settings are invalid; emulated mode will be blocked",
			"code", "SAVE-CARD-INVALID")
	}
	startup.SetPhase("Checking existing GameCube generation")
	gcManagerStarted := time.Now()
	gcLibrary, err := gamecube.NewLibraryManager(
		filepath.Join(dataDir, "gamecube", "library"), gcConfig)
	if err != nil {
		return failStartup("GameCube generation metadata could not be opened",
			fmt.Errorf("GameCube library configuration: %w", err))
	}
	gcProgress := gcLibrary.Progress()
	slog.Info("GameCube LibraryManager construction complete",
		"event", "startup", "phase", "GameCube LibraryManager construction",
		"phase_elapsed_ms", time.Since(gcManagerStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds(),
		"generation", gcProgress.GenerationID,
		"validation_state", gcProgress.Validation,
		"mapped_files", gcProgress.FilesMapped,
		"mapped_extents", gcProgress.ExtentCount)
	sessionTTL, err := time.ParseDuration(env("WIIBRIDGE_SESSION_TTL", "12h"))
	if err != nil {
		return failStartup("Session configuration is invalid",
			fmt.Errorf("WIIBRIDGE_SESSION_TTL: %w", err))
	}
	authStarted := time.Now()
	browserAuth, err := webauth.New(
		filepath.Join(dataDir, "auth"),
		env("WIIBRIDGE_UI_USERNAME", "admin"),
		env("WIIBRIDGE_UI_BOOTSTRAP_PASSWORD", "wiibridge"),
		sessionTTL,
	)
	if err != nil {
		return failStartup("Browser authentication could not be initialized",
			fmt.Errorf("browser authentication: %w", err))
	}
	slog.Info("Browser authentication initialized",
		"event", "startup", "phase", "browser-authentication initialization",
		"phase_elapsed_ms", time.Since(authStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	renderer, err := webui.New(webui.Functions(humanBytes))
	if err != nil {
		return failStartup("Web templates could not be initialized",
			fmt.Errorf("web templates: %w", err))
	}
	sqliteStarted := time.Now()
	database, err := store.Open(filepath.Join(dataDir, "wiibridge.sqlite3"))
	if err != nil {
		return failStartup("Persistent state could not be opened", err)
	}
	defer database.Close()
	slog.Info("SQLite open complete",
		"event", "startup", "phase", "SQLite open",
		"phase_elapsed_ms", time.Since(sqliteStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	startup.SetPhase("Scanning Wii library")
	metricsEnabled, err := boolEnv("WIIBRIDGE_PERFORMANCE_METRICS_ENABLED", true)
	if err != nil {
		return failStartup("Performance metrics configuration is invalid", err)
	}
	sessionHistory, err := boolEnv("WIIBRIDGE_SESSION_HISTORY_ENABLED", true)
	if err != nil {
		return failStartup("Performance session configuration is invalid", err)
	}
	maxSessions, err := intEnv("WIIBRIDGE_MAX_RETAINED_SESSIONS", 100)
	if err != nil || maxSessions < 1 || maxSessions > 1000 {
		return failStartup("Performance session retention is invalid",
			errors.New("WIIBRIDGE_MAX_RETAINED_SESSIONS must be between 1 and 1000"))
	}
	sessionRetentionDays, err := intEnv("WIIBRIDGE_SESSION_RETENTION_DAYS", 30)
	if err != nil || sessionRetentionDays < 1 || sessionRetentionDays > 3650 {
		return failStartup("Performance session age retention is invalid",
			errors.New("WIIBRIDGE_SESSION_RETENTION_DAYS must be between 1 and 3650"))
	}
	p99Warning, err := time.ParseDuration(env("WIIBRIDGE_PERF_P99_WARNING", "100ms"))
	if err != nil {
		return failStartup("Performance warning configuration is invalid", err)
	}
	memoryWarningPercent, err := intEnv("WIIBRIDGE_PERF_MEMORY_WARNING_PERCENT", 85)
	if err != nil || memoryWarningPercent < 1 || memoryWarningPercent > 100 {
		return failStartup("Performance memory warning configuration is invalid",
			errors.New("WIIBRIDGE_PERF_MEMORY_WARNING_PERCENT must be between 1 and 100"))
	}
	piMetricsInterval, err := time.ParseDuration(
		env("WIIBRIDGE_PI_METRICS_POLL_INTERVAL", "10s"))
	if err != nil || piMetricsInterval < 5*time.Second || piMetricsInterval > 5*time.Minute {
		return failStartup("Pi metrics interval is invalid",
			errors.New("WIIBRIDGE_PI_METRICS_POLL_INTERVAL must be between 5s and 5m"))
	}
	dashboardRefresh, err := time.ParseDuration(
		env("WIIBRIDGE_DASHBOARD_REFRESH_INTERVAL", "5s"))
	if err != nil || dashboardRefresh < 2*time.Second || dashboardRefresh > 30*time.Second {
		return failStartup("Dashboard refresh interval is invalid",
			errors.New("WIIBRIDGE_DASHBOARD_REFRESH_INTERVAL must be between 2s and 30s"))
	}
	metricsPersistence, err := time.ParseDuration(
		env("WIIBRIDGE_METRICS_PERSISTENCE_INTERVAL", "1m"))
	if err != nil || (metricsPersistence != 0 &&
		(metricsPersistence < 30*time.Second || metricsPersistence > time.Hour)) {
		return failStartup("Metrics persistence interval is invalid",
			errors.New("WIIBRIDGE_METRICS_PERSISTENCE_INTERVAL must be 0s or between 30s and 1h"))
	}
	metricsRegistry := perf.New(perf.Config{
		Enabled: metricsEnabled, SessionHistory: sessionHistory,
		MaxSessions: maxSessions, P99Warning: p99Warning,
		MemoryWarningPct: memoryWarningPercent,
	})
	if retained, loadErr := database.PerformanceSessions(maxSessions); loadErr == nil {
		metricsRegistry.ImportSessions(retained)
	}
	hostDescriptor := compat.NewDescriptor(
		"host", "", "", version, gitCommit, buildTime, compat.HostCapabilities())
	hostDescriptor.BuildDirty = buildDirty == "true"

	wiiScanStarted := time.Now()
	slog.Info("Wii library scan started",
		"event", "startup", "phase", "Wii scan started",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	result, sourceRecord, scanErr := scanWiiCatalog(database, root)
	if scanErr != nil {
		slog.Warn("Wii source unavailable; preserving prior complete catalog",
			"code", sourceRecord.FailureCode, "state", sourceRecord.State,
			"games", len(result.Games))
	}
	slog.Info("Wii library scan result", "games", len(result.Games),
		"rejected", len(result.Rejected), "candidate_files", result.FileCount,
		"event", "startup", "phase", "Wii scan completed",
		"phase_elapsed_ms", time.Since(wiiScanStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	startup.SetPhase("Building Wii virtual disk")
	wiiBuildStarted := time.Now()
	slog.Info("Wii virtual disk build started",
		"event", "startup", "phase", "Wii virtual disk build started",
		"wii_games", len(result.Games),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	disk, err := vdisk.Build("all", result.Games, version)
	if err != nil {
		return failStartup("Wii virtual disk build failed", err)
	}
	slog.Info("Wii virtual disk build completed",
		"event", "startup", "phase", "Wii virtual disk build completed",
		"wii_games", len(result.Games),
		"phase_elapsed_ms", time.Since(wiiBuildStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	startup.SetPhase("Finalizing validated exports")
	csrfSum := sha256.Sum256([]byte("wiibridge-host-csrf\x00" + token))
	a := &app{root: root, gcRoot: gcRoot, dataDir: dataDir, disk: disk, scan: result,
		tokenSum: sha256.Sum256([]byte(token)), started: hostStarted, store: database,
		failures: make(map[string]authFailure), csrf: hex.EncodeToString(csrfSum[:]),
		imports: make(map[string]importJob), gcLibrary: gcLibrary, gcMode: gcConfig.Mode,
		browser: browserAuth, web: renderer, gcStartupPhase: "Scanning GameCube library"}
	a.metricsRegistry = metricsRegistry
	a.sourceFailures = make(chan string, 8)
	a.gcSaveSelection = saveSelection
	if saveSelectionErr != nil {
		a.gcSaveError = "SAVE-CARD-INVALID: managed save settings require administrator repair"
	}
	a.source = sourceRecord
	a.hostDescriptor = hostDescriptor
	a.maxSessions = maxSessions
	a.sessionRetentionDays = sessionRetentionDays
	a.dashboardRefresh = dashboardRefresh
	a.metricsPersistence = metricsPersistence
	a.memoryLimit = detectContainerMemoryLimit(
		parseByteSize(env("WIIBRIDGE_MEMORY_LIMIT", "512m")))
	_ = database.LoadCompatibility(&a.compatibility)
	piManager, err := bridgecontrol.NewManager(
		os.Getenv("WIIBRIDGE_PI_URL"),
		os.Getenv("WIIBRIDGE_PI_ADMIN_TOKEN"),
		os.Getenv("WIIBRIDGE_PI_CERT"),
		filepath.Join(dataDir, "pi-address"),
	)
	if err != nil {
		return failStartup("Pi manager configuration is invalid",
			fmt.Errorf("automatic Pi switching configuration: %w", err))
	}
	if piManager != nil {
		if err = piManager.SetPollInterval(piMetricsInterval); err != nil {
			return failStartup("Pi metrics interval is invalid", err)
		}
	}
	a.pi = configuredPiController(piManager)
	slog.Info("Pi manager initialized",
		"event", "startup", "phase", "Pi manager initialized",
		"configured", a.pi != nil,
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	a.wii = &wiiExportProfile{app: a}
	disk.SetObserver(metricsRegistry, a.queueSourceFailure)
	a.exports, err = exportprofile.New(a.wii)
	if err != nil {
		return failStartup("Wii export manager could not be initialized", err)
	}
	snapshotStarted := time.Now()
	slog.Info("Snapshot persistence started",
		"event", "startup", "phase", "snapshot persistence started",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	if err := a.persistSnapshot(); err != nil {
		return failStartup("Wii snapshot could not be persisted", err)
	}
	slog.Info("Snapshot persistence completed",
		"event", "startup", "phase", "snapshot persistence completed",
		"phase_elapsed_ms", time.Since(snapshotStarted).Milliseconds(),
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	if piManager != nil {
		go piManager.Run(ctx)
	}
	nbdListener, err := net.Listen("tcp", env("WIIBRIDGE_NBD_LISTEN", ":10809"))
	if err != nil {
		return failStartup("NBD listener could not be opened", err)
	}
	nbdServer := &nbd.Server{
		BackendAcquirer: a.exports.BeginSession,
		TLS:             tlsConfig, ExportName: env("WIIBRIDGE_EXPORT", "all"),
		Deadline: 30 * time.Second, MaxRequest: 1 << 20, Metrics: metricsRegistry,
	}
	go func() { errs <- nbdServer.Serve(ctx, nbdListener) }()
	slog.Info("NBD listener opened",
		"event", "startup", "phase", "NBD listener opened",
		"elapsed_ms", time.Since(hostStarted).Milliseconds(),
		"wii_backend_ready", true)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.readyHealth)
	mux.Handle("GET /assets/", webui.Static())
	mux.HandleFunc("GET /login", a.loginForm)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.auth(a.logout))
	mux.HandleFunc("GET /account/password", a.auth(a.passwordForm))
	mux.HandleFunc("POST /account/password", a.auth(a.changePassword))
	mux.HandleFunc("GET /api/v1/status", a.auth(a.status))
	mux.HandleFunc("GET /api/v1/pi/status", a.auth(a.piStatus))
	mux.HandleFunc("GET /api/v1/compatibility", a.auth(a.compatibilityAPI))
	mux.HandleFunc("POST /api/v1/compatibility", a.auth(a.compatibilityAPI))
	mux.HandleFunc("GET /api/v1/sources", a.auth(a.sourceStatusAPI))
	mux.HandleFunc("GET /api/v1/sources/diagnostic", a.auth(a.sourceDiagnosticAPI))
	mux.HandleFunc("POST /api/v1/sources/acknowledge", a.auth(a.acknowledgeSourceRemoval))
	mux.HandleFunc("POST /api/v1/sources/relocate", a.auth(a.acceptSourceLocation))
	mux.HandleFunc("POST /api/v1/pi/address", a.auth(a.setPiAddress))
	mux.HandleFunc("GET /api/v1/scan", a.auth(a.scanResult))
	mux.HandleFunc("POST /api/v1/scan", a.auth(a.rescan))
	mux.HandleFunc("GET /api/v1/gamecube", a.auth(a.gamecubeResult))
	mux.HandleFunc("GET /api/v1/gamecube/imports", a.auth(a.gamecubeImports))
	mux.HandleFunc("GET /api/v1/gamecube/library", a.auth(a.gamecubeLibraryStatus))
	mux.HandleFunc("POST /api/v1/gamecube/library/build", a.auth(a.buildGameCubeLibrary))
	mux.HandleFunc("POST /api/v1/gamecube/library/cancel", a.auth(a.cancelGameCubeLibrary))
	mux.HandleFunc("POST /api/v1/gamecube/import", a.auth(a.importGameCube))
	mux.HandleFunc("POST /api/v1/gamecube/settings", a.auth(a.saveGameCubeSettings))
	mux.HandleFunc("GET /api/v1/gamecube/saves", a.auth(a.gameCubeSaveStatus))
	mux.HandleFunc("POST /api/v1/gamecube/saves/{action}", a.auth(a.gameCubeSaveAction))
	mux.HandleFunc("POST /api/v1/gamecube/saves/upload", a.auth(a.uploadGameCubeSave))
	mux.HandleFunc("GET /api/v1/gamecube/saves/download", a.auth(a.downloadGameCubeSave))
	mux.HandleFunc("POST /api/v1/export/{platform}", a.auth(a.selectExport))
	mux.HandleFunc("POST /api/v1/pi/{action}", a.auth(a.piPowerAction))
	mux.HandleFunc("POST /api/v1/pi/storage/{action}", a.auth(a.piStorageAction))
	mux.HandleFunc("GET /metrics", a.auth(a.metrics))
	mux.HandleFunc("GET /api/performance/summary", a.auth(a.performanceSummary))
	mux.HandleFunc("GET /api/performance/host", a.auth(a.performanceHost))
	mux.HandleFunc("GET /api/performance/pi", a.auth(a.performancePi))
	mux.HandleFunc("GET /api/performance/session/current", a.auth(a.performanceCurrentSession))
	mux.HandleFunc("GET /api/performance/sessions", a.auth(a.performanceSessions))
	mux.HandleFunc("GET /api/performance/sessions/{id}", a.auth(a.performanceSession))
	mux.HandleFunc("GET /api/performance/export", a.auth(a.performanceExport))
	mux.HandleFunc("GET /", a.auth(a.dashboard))
	a.mu.Lock()
	a.ready = sourceRecord.State == sourcehealth.StateAvailable
	a.mu.Unlock()
	handler.Set(mux)
	close(startupDone)
	slog.Info("Dashboard published",
		"event", "startup", "phase", "dashboard published",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	slog.Info("Wii Host readiness complete", "wii_games", len(result.Games),
		"event", "startup", "phase", "Host startup complete",
		"elapsed_ms", time.Since(hostStarted).Milliseconds())
	go a.initializeGameCube(ctx)
	go a.runAutomaticSaveBackups(ctx)
	go a.runSourceFailureReconciler(ctx)
	go a.runMetricsPersistence(ctx)
	select {
	case <-ctx.Done():
	case err = <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	_ = web.Shutdown(shutdown)
	_ = nbdListener.Close()
	a.switchMu.Lock()
	a.endPerformanceSession("host-shutdown")
	disconnectContext, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = a.waitForExportDisconnect(disconnectContext)
	disconnectCancel()
	if syncErr := a.syncGameCubeSaves(); err == nil {
		err = syncErr
	}
	if a.exports != nil {
		if disconnectErr := a.exports.Disconnect(); err == nil {
			err = disconnectErr
		}
	}
	a.switchMu.Unlock()
	return err
}

// configuredPiController prevents a nil *Manager from becoming a non-nil
// interface value. That distinction matters because dashboard feature checks
// use the interface to determine whether Pi coordination is enabled.
func configuredPiController(manager *bridgecontrol.Manager) piController {
	if manager == nil {
		return nil
	}
	return manager
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func int64Env(key string, fallback int64) (int64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func assertReadOnly(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cannot inspect read-only mount: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("library source must be a non-symlink directory")
	}
	var filesystem unix.Statfs_t
	if err = unix.Statfs(path, &filesystem); err != nil {
		return fmt.Errorf("cannot inspect read-only mount: %w", err)
	}
	if filesystem.Flags&unix.ST_RDONLY == 0 {
		return errors.New("library mount is writable; refusing startup")
	}
	return nil
}

func isUnavailableLibraryError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOTCONN) ||
		errors.Is(err, syscall.ESTALE) ||
		errors.Is(err, syscall.EIO) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH)
}

func mutualTLSConfig(certPath, keyPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("client CA contains no certificates")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert}, ClientCAs: pool,
		// libnbd 1.22/GnuTLS 3.8.9 does not select an X.509 client
		// certificate when Go requests it during a TLS 1.3 handshake.  TLS
		// 1.2 retains mutual authentication and is the interop-tested profile.
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
	}, nil
}

type sessionContextKey struct{}

const sessionCookie = "wiibridge_session"

func (a *app) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host == "" {
			host = r.RemoteAddr
		}
		if a.authLimited(host, false) {
			http.Error(w, "authentication rate limit", http.StatusTooManyRequests)
			return
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if session, valid := a.browser.Validate(cookie.Value); valid {
				if session.PasswordChange && r.URL.Path != "/account/password" &&
					r.URL.Path != "/logout" {
					if wantsHTML(r) {
						http.Redirect(w, r, "/account/password", http.StatusSeeOther)
					} else {
						http.Error(w, "password change required", http.StatusForbidden)
					}
					return
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					csrfValue := r.Header.Get("X-WiiBridge-CSRF")
					if csrfValue == "" {
						_ = r.ParseForm()
						csrfValue = r.Form.Get("csrf")
					}
					if subtle.ConstantTimeCompare(
						[]byte(csrfValue), []byte(session.CSRF)) != 1 {
						http.Error(w, "invalid CSRF token", http.StatusForbidden)
						return
					}
				}
				next(w, r.WithContext(context.WithValue(
					r.Context(), sessionContextKey{}, session)))
				return
			}
		}
		value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if user, password, ok := r.BasicAuth(); ok && user == "admin" {
			value = password
		}
		sum := sha256.Sum256([]byte(value))
		if len(value) == 0 || subtle.ConstantTimeCompare(sum[:], a.tokenSum[:]) != 1 {
			a.authLimited(host, true)
			if wantsHTML(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="WiiBridge", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		confirmed := r.Header.Get("X-WiiBridge-CSRF") == "1"
		if !confirmed && r.Method != http.MethodGet && r.Method != http.MethodHead {
			_ = r.ParseForm()
			confirmed = subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf")), []byte(a.csrf)) == 1
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !confirmed {
			http.Error(w, "missing CSRF confirmation", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html") ||
		!strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/metrics"
}

func browserSession(r *http.Request) (webauth.Session, bool) {
	session, ok := r.Context().Value(sessionContextKey{}).(webauth.Session)
	return session, ok
}

func (a *app) loginForm(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if _, valid := a.browser.Validate(cookie.Value); valid {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.web.Execute(w, "login.html", map[string]any{"CSRF": a.csrf})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(a.csrf)) != 1 {
		http.Error(w, "invalid login request", http.StatusForbidden)
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	if a.authLimited(host, false) {
		http.Error(w, "authentication rate limit", http.StatusTooManyRequests)
		return
	}
	valid, force := a.browser.Authenticate(
		strings.TrimSpace(r.FormValue("username")), r.FormValue("password"))
	if !valid {
		a.authLimited(host, true)
		w.WriteHeader(http.StatusUnauthorized)
		_ = a.web.Execute(w, "login.html", map[string]any{
			"CSRF": a.csrf, "Error": "Invalid username or password.",
		})
		return
	}
	session, err := a.browser.NewSession(force)
	if err != nil {
		http.Error(w, "login unavailable", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: session.ID, Path: "/", Expires: session.Expires,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	if force {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if session, ok := browserSession(r); ok {
		a.browser.Logout(session.ID)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *app) passwordForm(w http.ResponseWriter, r *http.Request) {
	session, ok := browserSession(r)
	if !ok {
		http.Error(w, "browser session required", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.web.Execute(w, "change-password.html", map[string]any{"CSRF": session.CSRF})
}

func (a *app) changePassword(w http.ResponseWriter, r *http.Request) {
	session, ok := browserSession(r)
	if !ok {
		http.Error(w, "browser session required", http.StatusForbidden)
		return
	}
	if r.FormValue("password") != r.FormValue("confirm") {
		w.WriteHeader(http.StatusBadRequest)
		_ = a.web.Execute(w, "change-password.html", map[string]any{
			"CSRF": session.CSRF, "Error": "The new passwords do not match.",
		})
		return
	}
	if err := a.browser.ChangePassword(
		session.ID, r.FormValue("current"), r.FormValue("password")); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = a.web.Execute(w, "change-password.html", map[string]any{
			"CSRF": session.CSRF, "Error": err.Error(),
		})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *app) authLimited(host string, failed bool) bool {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	now := time.Now()
	entry := a.failures[host]
	if now.After(entry.reset) {
		entry = authFailure{reset: now.Add(time.Minute)}
	}
	if failed {
		entry.count++
		a.failures[host] = entry
		if len(a.failures) > 4096 {
			clear(a.failures)
		}
	}
	return entry.count >= 10
}

func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "healthy", "phase": "Ready", "version": version,
		"revision": gitCommit, "built": buildTime,
	})
}

func (a *app) readyHealth(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	exports := a.exports
	wiiReady := a.ready && a.disk != nil
	gcAvailable := a.librarySourceLocked("gamecube").State == sourcehealth.StateAvailable
	a.mu.RUnlock()
	ready, phase := false, "Library source unavailable"
	if exports != nil {
		if exports.Platform() == "gamecube" {
			_, validated := a.gcLibrary.ValidatedSummary()
			ready = gcAvailable && validated
			phase = "GameCube library validation required"
		} else {
			ready = wiiReady && a.wii.Validate() == nil
			phase = "Wii library source unavailable"
		}
	}
	if !ready {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"status": "not-ready", "phase": phase})
		return
	}
	writeJSON(w, map[string]string{"status": "ready", "phase": "Ready"})
}

func (a *app) initializeGameCube(ctx context.Context) {
	started := time.Now()
	slog.Info("GameCube background initialization started",
		"event", "startup", "phase", "GameCube scan started",
		"elapsed_ms", 0)
	a.scanMu.Lock()
	root := a.libraryRoot("gamecube")
	preflight, preflightErr := sourcehealth.Preflight(root, previousPlatformSource(a.store, root, "gamecube"))
	sourceRecord := preflight.Record
	if preflightErr != nil {
		_ = a.store.UpsertSource(sourceRecord)
	}
	result, sourceRecord, err := scanGameCubeCatalog(a.store, root, sourceRecord, root == a.root)
	a.mu.Lock()
	a.gcScan, a.gcSource = result, sourceRecord
	if err != nil {
		a.gcStartupPhase, a.gcStartupError = "Source offline", sourceRecord.FailureMessage
	} else {
		a.gcStartupPhase, a.gcStartupError = "Checking existing GameCube generation", ""
	}
	a.mu.Unlock()
	a.scanMu.Unlock()
	if err != nil {
		slog.Warn("GameCube source unavailable; prior catalog and generation retained", "code", sourceRecord.FailureCode, "games_preserved", len(result.Games))
		return
	}
	slog.Info("GameCube library scan result", "games", len(result.Games),
		"rejected", len(result.Rejected), "candidate_files", result.FileCount,
		"event", "startup", "phase", "GameCube scan completed",
		"elapsed_ms", time.Since(started).Milliseconds())

	if _, managedErr := a.gcLibrary.ManagedActive(); managedErr == nil {
		if recheckErr := a.gcLibrary.RecheckActive(); recheckErr != nil {
			if errors.Is(recheckErr, gamecube.ErrGameCubeSourceUnavailable) || errors.Is(recheckErr, gamecube.ErrGameCubeSourceChanged) {
				a.mu.Lock()
				a.gcUpdate = true
				a.gcStartupPhase, a.gcStartupError = "Rebuild required", "The previous GameCube generation refers to moved or changed files. Rebuild from the scanned library."
				a.mu.Unlock()
				return
			}
			a.mu.Lock()
			a.gcStartupPhase = "Startup failed"
			a.gcStartupError = boundedLogError(recheckErr)
			a.mu.Unlock()
			return
		}
	}
	progress := a.gcLibrary.Progress()
	if progress.State == "Failed" {
		a.mu.Lock()
		a.gcStartupPhase = "Startup failed"
		a.gcStartupError = progress.Error
		a.mu.Unlock()
		slog.Error("GameCube fast generation validation failed",
			"event", "startup", "phase", "GameCube fast validation failed",
			"elapsed_ms", time.Since(started).Milliseconds(),
			"error", progress.Error)
		return
	}
	if progress.Validation == "pending" || progress.Validation == "validating" {
		a.mu.Lock()
		a.gcStartupPhase = "Validating GameCube library"
		a.mu.Unlock()
		if err = a.gcLibrary.StartActiveValidation(ctx); err != nil {
			a.mu.Lock()
			a.gcStartupPhase = "Startup failed"
			a.gcStartupError = "GameCube deep validation could not start"
			a.mu.Unlock()
			slog.Error("GameCube deep validation could not start",
				"event", "startup", "phase", "GameCube validation failed",
				"elapsed_ms", time.Since(started).Milliseconds(),
				"error", boundedLogError(err))
			return
		}
		slog.Info("GameCube deep validation started",
			"event", "startup", "phase", "GameCube deep validation started",
			"generation", a.gcLibrary.Progress().GenerationID,
			"files_total", a.gcLibrary.Progress().ValidationTotal,
			"elapsed_ms", time.Since(started).Milliseconds())
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastLog := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				progress = a.gcLibrary.Progress()
				if time.Since(lastLog) >= 30*time.Second {
					slog.Info("GameCube deep validation still running",
						"files_completed", progress.ValidationFiles,
						"total_files", progress.ValidationTotal,
						"bytes_hashed", progress.ValidationBytes,
						"elapsed_ms", time.Since(started).Milliseconds())
					lastLog = time.Now()
				}
				switch progress.State {
				case "Ready":
					a.mu.Lock()
					a.gcStartupPhase = "Ready"
					a.gcStartupError = ""
					a.mu.Unlock()
					slog.Info("GameCube background initialization complete",
						"event", "startup", "phase", "GameCube validation completed",
						"elapsed_ms", time.Since(started).Milliseconds(),
						"bytes_hashed", progress.ValidationBytes)
					return
				case "Failed", "Canceled":
					a.mu.Lock()
					a.gcStartupPhase = "Startup failed"
					a.gcStartupError = progress.Error
					a.mu.Unlock()
					slog.Error("GameCube deep validation failed",
						"event", "startup", "phase", "GameCube validation failed",
						"elapsed_ms", time.Since(started).Milliseconds(),
						"error", progress.Error)
					return
				}
			}
		}
	}
	a.mu.Lock()
	a.gcStartupPhase = "Ready"
	a.gcStartupError = ""
	a.mu.Unlock()
	slog.Info("GameCube background initialization complete",
		"event", "startup", "phase", "GameCube validation receipt current",
		"elapsed_ms", time.Since(started).Milliseconds(),
		"deep_validation", "receipt-current")
}

func boundedLogError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func (a *app) status(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	snapshot := a.disk.Snapshot()
	wiiGames := len(a.scan.Games)
	wiiRejected := len(a.scan.Rejected)
	gameCubeGames := len(a.gcScan.Games)
	gameCubeRejected := len(a.gcScan.Rejected)
	gameCubeStartupPhase := a.gcStartupPhase
	gameCubeStartupError := a.gcStartupError
	started := a.started
	a.mu.RUnlock()
	activeLibrary, libraryReady := a.gcLibrary.ValidatedSummary()
	writeJSON(w, map[string]any{
		"version": version, "revision": gitCommit, "built": buildTime,
		"snapshot": snapshot, "games": wiiGames,
		"rejected": wiiRejected, "gamecube_games": gameCubeGames,
		"gamecube_rejected": gameCubeRejected, "platform": a.exports.Platform(),
		"export_state": a.exports.State(), "automatic_switching": a.pi != nil,
		"gamecube_library_ready": libraryReady,
		"gamecube_library_generation": func() string {
			if libraryReady {
				return activeLibrary.GenerationID
			}
			return ""
		}(),
		"gamecube_library_build": a.gcLibrary.Progress(),
		"gamecube_startup_phase": gameCubeStartupPhase,
		"gamecube_startup_error": gameCubeStartupError,
		"uptime_seconds":         int(time.Since(started).Seconds()),
	})
}

func (a *app) piStatus(w http.ResponseWriter, r *http.Request) {
	if a.pi == nil {
		http.Error(w, "Pi connection is not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := a.pi.Status(ctx)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{
			"connected": false, "error": "Pi status unavailable",
		})
		return
	}
	writeJSON(w, map[string]any{"connected": true, "pi": status})
}

func (a *app) setPiAddress(w http.ResponseWriter, r *http.Request) {
	if a.pi == nil {
		http.Error(w, "Pi connection is not configured", http.StatusServiceUnavailable)
		return
	}
	address := strings.TrimSpace(r.FormValue("address"))
	if net.ParseIP(address) == nil {
		http.Error(w, "Pi address must be a literal IPv4 or IPv6 address", http.StatusBadRequest)
		return
	}
	if err := a.pi.SetAddress(r.Context(), address); err != nil {
		slog.Warn("cannot update Pi address", "error", err)
		http.Error(w, "Pi address update failed", http.StatusInternalServerError)
		return
	}
	respondAction(w, r, http.StatusOK,
		map[string]string{"status": "updated", "address": a.pi.Address()},
		"Raspberry Pi address updated; live status is reconnecting.", "all")
}

func (a *app) scanResult(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, a.scan)
}

func (a *app) gamecubeResult(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, a.gcScan)
}

func (a *app) gamecubeImports(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, a.imports)
}

func (a *app) gamecubeLibraryStatus(w http.ResponseWriter, _ *http.Request) {
	progress := a.gcLibrary.Progress()
	active, ready := a.gcLibrary.ValidatedSummary()
	a.mu.Lock()
	if ready && progress.State == "Ready" {
		a.gcUpdate = false
	}
	updateAvailable := a.gcUpdate
	a.mu.Unlock()
	response := map[string]any{
		"progress": progress, "ready": ready, "update_available": updateAvailable,
		"memory_card_mode": a.gcMode,
	}
	if ready {
		response["generation"] = active.GenerationID
		response["titles"] = active.TitleCount
		response["discs"] = active.DiscCount
	}
	writeJSON(w, response)
}

func (a *app) buildGameCubeLibrary(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	games := append([]gamecube.Game(nil), a.gcScan.Games...)
	sourceState := a.librarySourceLocked("gamecube").State
	saveError := a.gcSaveError
	a.mu.RUnlock()
	if sourceState != sourcehealth.StateAvailable {
		http.Error(w, "GameCube source is unavailable; the prior generation is retained",
			http.StatusServiceUnavailable)
		return
	}
	if saveError != "" && a.gcMode.IsLibraryEmulated() {
		http.Error(w, saveError, http.StatusConflict)
		return
	}
	games = playableGameCubeGames(games)
	if len(games) == 0 {
		http.Error(w, "no playable GameCube sources are available", http.StatusConflict)
		return
	}
	if err := a.gcLibrary.StartBuild(context.Background(), games); err != nil {
		http.Error(w, "GameCube library build is already active", http.StatusConflict)
		return
	}
	respondAction(w, r, http.StatusAccepted,
		map[string]string{"status": "Building"},
		"GameCube library build started.", "gamecube")
}

func (a *app) cancelGameCubeLibrary(w http.ResponseWriter, r *http.Request) {
	if !a.gcLibrary.Cancel() {
		http.Error(w, "no GameCube library build is active", http.StatusConflict)
		return
	}
	respondAction(w, r, http.StatusAccepted,
		map[string]string{"status": "canceling"},
		"GameCube library build cancellation requested.", "gamecube")
}

func (a *app) rescan(w http.ResponseWriter, r *http.Request) {
	a.rescanLibraries(w, r, false)
}

func (a *app) findGameCube(id string, revision byte) (gamecube.Game, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, game := range a.gcScan.Games {
		if game.ID == id && game.Revision == revision {
			return game, true
		}
	}
	return gamecube.Game{}, false
}

func (a *app) importGameCube(w http.ResponseWriter, r *http.Request) {
	id := strings.ToUpper(strings.TrimSpace(r.FormValue("id")))
	revision64, err := strconv.ParseUint(r.FormValue("revision"), 10, 8)
	if err != nil {
		http.Error(w, "invalid revision", http.StatusBadRequest)
		return
	}
	game, ok := a.findGameCube(id, byte(revision64))
	if !ok {
		http.Error(w, "validated GameCube game not found", http.StatusNotFound)
		return
	}
	settings, err := gamecube.LoadSettings(filepath.Join(a.dataDir, "gamecube"), id, byte(revision64))
	if err != nil {
		http.Error(w, "settings invalid", http.StatusConflict)
		return
	}
	key := gamecube.CacheKey(game, settings.MemoryCard)
	a.mu.Lock()
	if job, exists := a.imports[key]; exists && (job.Status == "queued" || job.Status == "building") {
		a.mu.Unlock()
		http.Error(w, "import already active", http.StatusConflict)
		return
	}
	a.imports[key] = importJob{Status: "queued"}
	a.mu.Unlock()
	go func() {
		a.mu.Lock()
		a.imports[key] = importJob{Status: "building"}
		a.mu.Unlock()
		manifest, buildErr := gamecube.BuildVolume(context.Background(),
			filepath.Join(a.dataDir, "gamecube", "cache"), game, settings.MemoryCard)
		a.mu.Lock()
		defer a.mu.Unlock()
		if buildErr != nil {
			a.imports[key] = importJob{Status: "failed", Error: buildErr.Error()}
			return
		}
		a.imports[key] = importJob{Status: "ready", Manifest: &manifest}
	}()
	respondAction(w, r, http.StatusAccepted,
		map[string]string{"cache_key": key, "status": "queued"},
		"GameCube export preparation was queued.", "gamecube")
}

func (a *app) saveGameCubeSettings(w http.ResponseWriter, r *http.Request) {
	id := strings.ToUpper(strings.TrimSpace(r.FormValue("id")))
	revision64, err := strconv.ParseUint(r.FormValue("revision"), 10, 8)
	if err != nil {
		http.Error(w, "invalid revision", http.StatusBadRequest)
		return
	}
	if _, ok := a.findGameCube(id, byte(revision64)); !ok {
		http.Error(w, "validated GameCube game not found", http.StatusNotFound)
		return
	}
	settings := gamecube.DefaultSettings(id, byte(revision64))
	settings.MemoryCard = gamecube.MemoryCardMode(r.FormValue("memory_card"))
	size, err := strconv.ParseUint(r.FormValue("memory_card_size"), 10, 8)
	if err != nil {
		http.Error(w, "invalid memory-card size", http.StatusBadRequest)
		return
	}
	settings.MemoryCardSize = byte(size)
	settings.MultiCard = r.FormValue("multi_card") == "1"
	settings.NativeControl = r.FormValue("native_control") == "1"
	settings.VideoMode = r.FormValue("video_mode")
	settings.Progressive = r.FormValue("progressive") == "1"
	settings.PAL50Patch = r.FormValue("pal50_patch") == "1"
	settings.Widescreen = r.FormValue("widescreen") == "1"
	if value := r.FormValue("video_width"); value != "" {
		settings.VideoWidth, err = strconv.Atoi(value)
		if err != nil {
			http.Error(w, "invalid video width", http.StatusBadRequest)
			return
		}
	}
	if value := r.FormValue("video_offset"); value != "" {
		settings.VideoOffset, err = strconv.Atoi(value)
		if err != nil {
			http.Error(w, "invalid video offset", http.StatusBadRequest)
			return
		}
	}
	settings.Cheats = r.FormValue("cheats") == "1"
	settings.UseIPL = r.FormValue("use_ipl") == "1"
	settings.ControllerMode = r.FormValue("controller_mode")
	settings.DiscSpeed = r.FormValue("disc_speed")
	settings.ReturnToLoader = r.FormValue("return_to_loader") == "1"
	if err = gamecube.SaveSettings(filepath.Join(a.dataDir, "gamecube"), settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondAction(w, r, http.StatusOK, settings,
		"Compatibility settings saved.", "gamecube")
}

func (a *app) selectExport(w http.ResponseWriter, r *http.Request) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	platform := r.PathValue("platform")
	if platform != "wii" && platform != "gamecube" {
		http.Error(w, "unsupported platform", http.StatusBadRequest)
		return
	}
	automatic := a.pi != nil
	if automatic {
		operation := compat.OperationWiiConnect
		if platform == "gamecube" {
			if a.gcMode.IsLibraryEmulated() {
				operation = compat.OperationGameCubeEmulated
			} else {
				operation = compat.OperationGameCubePhysical
			}
		}
		status, compatibility, err := a.freshCompatibility(r, operation)
		if err != nil {
			slog.Warn("automatic export switch blocked",
				"platform", platform, "compatibility", compatibility.Status)
			http.Error(w,
				"automatic switching is blocked by the Host/firmware compatibility check",
				http.StatusServiceUnavailable)
			return
		}
		if err = validatePiSwitchReadiness(status); err != nil {
			slog.Warn("automatic export switch blocked",
				"platform", platform, "reason", err.Error())
			http.Error(w, "automatic switching is unavailable: "+err.Error(),
				http.StatusConflict)
			return
		}
	}
	var next exportprofile.Profile = a.wii
	var selectedManifest *gamecube.VolumeManifest
	var selectedLibrary *gamecube.LibraryManifest
	var selectedSaves *gamecube.SaveStore
	connectAction := "connect-wii"
	if platform == "gamecube" {
		a.mu.RLock()
		sourceState, saveError := a.librarySourceLocked("gamecube").State, a.gcSaveError
		a.mu.RUnlock()
		if sourceState != sourcehealth.StateAvailable {
			http.Error(w, "GameCube source is unavailable; its generation and saves were retained",
				http.StatusServiceUnavailable)
			return
		}
		if saveError != "" && a.gcMode.IsLibraryEmulated() {
			http.Error(w, saveError, http.StatusConflict)
			return
		}
		manifest, err := a.gcLibrary.Active()
		if err != nil {
			http.Error(w, "complete GameCube library is not ready; build it before activation",
				http.StatusConflict)
			return
		}
		if manifest.Mode != a.gcMode ||
			manifest.ReadOnly != (a.gcMode == gamecube.MemoryCardPhysical) {
			http.Error(w, "complete GameCube library was built for a different memory-card mode; rebuild it before activation",
				http.StatusConflict)
			return
		}
		backend, saves, err := gamecube.OpenLibraryBackendAndSaveStore(
			a.gcLibrary.Root(), manifest, a.metricsRegistry)
		if err != nil {
			http.Error(w, "GameCube backend open failed", http.StatusInternalServerError)
			return
		}
		backend.SetSourceFailureHandler(a.queueGameCubeSourceFailure)
		next = &exportprofile.BasicProfile{
			Name: "gamecube", BlockBackend: backend,
			Immutable: a.gcMode == gamecube.MemoryCardPhysical,
			ValidateProfile: func() error {
				validated, validateErr := a.gcLibrary.Active()
				if validateErr != nil {
					return validateErr
				}
				if validated.GenerationID != manifest.GenerationID {
					return errors.New("GameCube active generation changed during activation")
				}
				return nil
			},
			CloseProfile: backend.Close,
		}
		if a.gcMode.IsLibraryEmulated() {
			connectAction = "connect-gamecube-emulated"
		} else {
			connectAction = "connect-gamecube-physical"
		}
		selectedLibrary = &manifest
		selectedSaves = saves
	}
	a.mu.RLock()
	previousManifest := a.activeGC
	previousLibrary := a.activeGCLibrary
	previousSaves := a.gcSaves
	a.mu.RUnlock()
	if automatic {
		if err := a.pi.Action(r.Context(), "detach"); err != nil {
			_ = next.Close()
			http.Error(w, "automatic switch stopped before changing the Host export",
				http.StatusBadGateway)
			return
		}
		if err := a.pi.Action(r.Context(), "disconnect"); err != nil {
			_ = next.Close()
			http.Error(w, "automatic switch could not disconnect NBD; USB remains detached",
				http.StatusBadGateway)
			return
		}
	}
	if err := a.waitForExportDisconnect(r.Context()); err != nil {
		if next != a.wii {
			_ = next.Close()
		}
		if automatic {
			http.Error(w, "Pi disconnected but the Host still has active I/O; USB remains detached",
				http.StatusConflict)
		} else {
			http.Error(w, "disconnect the Pi NBD session before switching",
				http.StatusConflict)
		}
		return
	}
	if platform == "wii" && previousLibrary != nil &&
		previousLibrary.Mode.IsLibraryEmulated() && previousSaves != nil {
		if err := previousSaves.Sync(); err != nil {
			_ = next.Close()
			http.Error(w, "GameCube save flush failed; USB remains detached",
				http.StatusInternalServerError)
			return
		}
		for _, saveStatus := range previousSaves.Statuses() {
			if _, err := previousSaves.Backup(saveStatus.ID, "detach"); err != nil {
				_ = next.Close()
				http.Error(w, "GameCube save backup failed; USB remains detached",
					http.StatusInternalServerError)
				return
			}
		}
	}
	a.endPerformanceSession("switched")
	if err := a.exports.Select(next); err != nil {
		_ = next.Close()
		http.Error(w, "export selection failed: "+err.Error(), http.StatusConflict)
		return
	}
	a.mu.Lock()
	a.activeGC = selectedManifest
	a.activeGCLibrary = selectedLibrary
	a.gcSaves = selectedSaves
	a.mu.Unlock()
	if platform == "wii" && previousManifest != nil {
		if _, err := gamecube.BackupMemoryCards(*previousManifest,
			filepath.Join(a.dataDir, "gamecube", "save-backups"),
			gamecube.DefaultSaveBackupRetention); err != nil {
			http.Error(w, "Wii mode selected, but GameCube save backup failed: "+err.Error(),
				http.StatusInternalServerError)
			return
		}
	}
	if automatic {
		if err := a.pi.Action(r.Context(), connectAction); err != nil {
			a.safePiDisconnect()
			http.Error(w, "Host export selected, but Pi reconnect failed; USB remains detached",
				http.StatusBadGateway)
			return
		}
		if err := a.pi.Action(r.Context(), "attach"); err != nil {
			a.safePiDisconnect()
			http.Error(w, "Pi connected, but USB attach failed and was rolled back",
				http.StatusBadGateway)
			return
		}
		a.startPerformanceSession()
	}
	notice := "Host export switched to " + platform + "."
	if automatic {
		notice = "Pi safely detached, switched to " + platform + ", reconnected, and reattached USB."
	}
	respondAction(w, r, http.StatusOK,
		map[string]any{"platform": a.exports.Platform(), "state": a.exports.State()},
		notice, platform)
}

func validatePiSwitchReadiness(status bridgecontrol.Status) error {
	if !status.BoardOK || status.State == "wrong-board-recovery" {
		return errors.New("Raspberry Pi board validation has not completed")
	}
	if !status.Provisioned || !status.WiFiReady || status.State != "ready" {
		return errors.New("Raspberry Pi provisioning is not ready")
	}
	if status.USBController == "" || status.USBController == "none" ||
		status.USBState == "" || status.USBState == "unknown" ||
		status.USBState == "unavailable" {
		return errors.New("Raspberry Pi USB gadget controller is unavailable")
	}
	return nil
}

func (a *app) waitForExportDisconnect(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = a.exports.Disconnect(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return err
}

func (a *app) safePiDisconnect() {
	if a.pi == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.pi.Action(ctx, "detach")
	_ = a.pi.Action(ctx, "disconnect")
}

func (a *app) currentConnectAction() string {
	if a.exports.Platform() == "wii" {
		return "connect-wii"
	}
	if a.gcMode.IsLibraryEmulated() {
		return "connect-gamecube-emulated"
	}
	return "connect-gamecube-physical"
}

func (a *app) piStorageAction(w http.ResponseWriter, r *http.Request) {
	if a.pi == nil {
		http.Error(w, "Pi coordination is not configured", http.StatusServiceUnavailable)
		return
	}
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	action := r.PathValue("action")
	operation := compat.OperationStatus
	switch action {
	case "detach":
		operation = compat.OperationUSBDetach
	case "disconnect":
		operation = compat.OperationSafeDisconnect
	case "connect":
		if a.exports.Platform() == "wii" {
			operation = compat.OperationWiiConnect
		} else if a.gcMode.IsLibraryEmulated() {
			operation = compat.OperationGameCubeEmulated
		} else {
			operation = compat.OperationGameCubePhysical
		}
	case "attach":
		operation = compat.OperationUSBAttach
	case "reconcile":
		operation = compat.OperationAutomaticSwitch
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	if _, result, compatibilityErr := a.freshCompatibility(r, operation); compatibilityErr != nil {
		writeJSONStatus(w, http.StatusConflict, result)
		return
	}
	var err error
	switch action {
	case "detach":
		err = a.pi.Action(r.Context(), "detach")
		if err == nil {
			err = a.syncGameCubeSaves()
		}
		if err == nil {
			a.endPerformanceSession("detached")
		}
	case "disconnect":
		if err = a.pi.Action(r.Context(), "detach"); err == nil {
			err = a.syncGameCubeSaves()
		}
		if err == nil {
			err = a.pi.Action(r.Context(), "disconnect")
		}
		if err == nil {
			a.endPerformanceSession("disconnected")
		}
	case "connect":
		err = a.pi.Action(r.Context(), a.currentConnectAction())
	case "attach":
		var status bridgecontrol.Status
		status, err = a.pi.Status(r.Context())
		if err == nil && status.ExportMode != strings.TrimPrefix(
			a.currentConnectAction(), "connect-") {
			err = errors.New("Pi connection mode does not match the selected Host library")
		}
		if err == nil {
			err = a.pi.Action(r.Context(), "attach")
		}
		if err == nil {
			a.startPerformanceSession()
		}
	case "reconcile":
		if err = a.pi.Action(r.Context(), "detach"); err == nil {
			err = a.syncGameCubeSaves()
		}
		if err == nil {
			err = a.pi.Action(r.Context(), "disconnect")
		}
		if err == nil {
			err = a.waitForExportDisconnect(r.Context())
		}
		if err == nil {
			err = a.pi.Action(r.Context(), a.currentConnectAction())
		}
		if err == nil {
			err = a.pi.Action(r.Context(), "attach")
		}
		if err == nil {
			a.endPerformanceSession("reconciled")
			a.startPerformanceSession()
		}
		if err != nil {
			a.safePiDisconnect()
		}
	default:
		http.Error(w, "unsupported storage control", http.StatusBadRequest)
		return
	}
	if err != nil {
		slog.Warn("Pi storage control failed", "control", action)
		http.Error(w, "Pi storage control failed; USB was left detached where possible",
			http.StatusBadGateway)
		return
	}
	respondAction(w, r, http.StatusOK, map[string]string{"status": "ok"},
		"Raspberry Pi storage control completed.", a.exports.Platform())
}

func (a *app) piPowerAction(w http.ResponseWriter, r *http.Request) {
	if a.pi == nil {
		http.Error(w, "Pi controls require automatic switching configuration",
			http.StatusServiceUnavailable)
		return
	}
	action := r.PathValue("action")
	helperAction, label := "", ""
	switch action {
	case "reboot":
		helperAction, label = "reboot", "reboot"
	case "shutdown":
		helperAction, label = "poweroff", "shut down"
	default:
		http.Error(w, "unsupported Pi power action", http.StatusBadRequest)
		return
	}
	if r.FormValue("confirm") != action {
		http.Error(w, "explicit Pi power confirmation is required", http.StatusBadRequest)
		return
	}
	operation := compat.OperationReboot
	if action == "shutdown" {
		operation = compat.OperationShutdown
	}
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	if _, result, compatibilityErr := a.freshCompatibility(r, operation); compatibilityErr != nil {
		writeJSONStatus(w, http.StatusConflict, result)
		return
	}
	if err := a.pi.Action(r.Context(), helperAction); err != nil {
		slog.Warn("Pi power action failed", "action", helperAction)
		http.Error(w, "Pi "+label+" failed", http.StatusBadGateway)
		return
	}
	respondAction(w, r, http.StatusOK, map[string]string{"status": action},
		"Pi "+label+" requested. USB and NBD were disconnected first.", a.exports.Platform())
}

func (a *app) restoreGameCubeSave(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	manifest := a.activeGC
	a.mu.RUnlock()
	if manifest == nil || a.exports.Platform() != "gamecube" {
		http.Error(w, "select the matching GameCube export before restoring its save",
			http.StatusConflict)
		return
	}
	if err := a.exports.Disconnect(); err != nil {
		http.Error(w, "detach USB and disconnect Pi NBD before restore: "+err.Error(),
			http.StatusConflict)
		return
	}
	backupPath := filepath.Clean(r.FormValue("backup"))
	available, err := gamecube.ListSaveBackups(
		filepath.Join(a.dataDir, "gamecube", "save-backups"),
		manifest.Game.ID, manifest.Game.Revision)
	if err != nil {
		http.Error(w, "cannot list save backups", http.StatusInternalServerError)
		return
	}
	allowed := false
	for _, backup := range available {
		if backup.Path == backupPath {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "save backup is not in the managed backup set", http.StatusBadRequest)
		return
	}
	if _, err = gamecube.BackupMemoryCards(*manifest,
		filepath.Join(a.dataDir, "gamecube", "save-backups"),
		gamecube.DefaultSaveBackupRetention+1); err != nil {
		http.Error(w, "current save backup failed; restore refused: "+err.Error(),
			http.StatusConflict)
		return
	}
	if err = gamecube.RestoreMemoryCard(*manifest, backupPath, r.FormValue("name")); err != nil {
		http.Error(w, "save restore failed: "+err.Error(), http.StatusConflict)
		return
	}
	respondAction(w, r, http.StatusOK,
		map[string]string{"status": "restored", "state": string(a.exports.State())},
		"Latest validated memory-card backup restored.", "gamecube")
}

func (a *app) persistSnapshot() error {
	a.mu.RLock()
	snapshot := a.disk.Snapshot()
	a.mu.RUnlock()
	if err := a.writeSnapshotFile(snapshot); err != nil {
		return err
	}
	return a.store.Publish(snapshot)
}

func (a *app) writeSnapshotFile(snapshot model.Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(a.dataDir, "snapshots")
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, snapshot.SnapshotID+".json")
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return nil
}

func (a *app) metrics(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	games, rejections := len(a.scan.Games)+len(a.gcScan.Games),
		len(a.scan.Rejected)+len(a.gcScan.Rejected)
	sourceState := a.source.State
	a.mu.RUnlock()
	snapshot := a.performanceSnapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "wiibridge_catalog_games %d\n", games)
	fmt.Fprintf(w, "wiibridge_scan_rejections %d\n", rejections)
	fmt.Fprintf(w, "wiibridge_source_state{state=%q} 1\n", sourceState)
	fmt.Fprintf(w, "wiibridge_source_read_operations %d\n",
		snapshot.Source.Counters["read_operations"])
	fmt.Fprintf(w, "wiibridge_source_bytes_read %d\n",
		snapshot.Source.Counters["bytes_read"])
	fmt.Fprintf(w, "wiibridge_source_read_errors %d\n",
		snapshot.Source.Counters["read_errors"])
	fmt.Fprintf(w, "wiibridge_nbd_active_connections %d\n",
		snapshot.NBD.Counters["active_connections"])
	fmt.Fprintf(w, "wiibridge_nbd_read_requests %d\n",
		snapshot.NBD.Counters["read_requests"])
	fmt.Fprintf(w, "wiibridge_nbd_bytes_sent %d\n",
		snapshot.NBD.Counters["bytes_sent"])
	fmt.Fprintf(w, "wiibridge_save_dirty_blocks %d\n",
		snapshot.Save.Counters["dirty_blocks"])
	fmt.Fprintf(w, "wiibridge_save_journal_bytes %d\n",
		snapshot.Save.Counters["journal_bytes"])
	fmt.Fprintf(w, "wiibridge_save_flush_failures %d\n",
		snapshot.Save.Counters["flush_failures"])
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	wiiGames := append([]model.Game(nil), a.scan.Games...)
	gameCubeGames := append([]gamecube.Game(nil), a.gcScan.Games...)
	wiiRejections := append([]scanner.Rejection(nil), a.scan.Rejected...)
	gameCubeRejections := append([]gamecube.Rejection(nil), a.gcScan.Rejected...)
	libraryRoot := a.root
	gcUpdate := a.gcUpdate
	sourceRecord := a.source
	gcSourceRecord := a.librarySourceLocked("gamecube")
	compatibility := a.compatibility
	saveSelection := a.gcSaveSelection
	saveError := a.gcSaveError
	a.mu.RUnlock()
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	matches := func(title, id string) bool {
		return query == "" ||
			strings.Contains(strings.ToLower(title), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(id), strings.ToLower(query))
	}
	filteredWii := make([]model.Game, 0, len(wiiGames))
	for _, game := range wiiGames {
		if matches(game.Title, game.ID) {
			filteredWii = append(filteredWii, game)
		}
	}
	type gcRow struct {
		gamecube.Game
		Included bool
	}
	active, activeReady := a.gcLibrary.ValidatedSummary()
	included := make(map[string]bool)
	if activeReady {
		for _, title := range active.Titles {
			included[fmt.Sprintf("%s:%d", title.ID, title.Revision)] = true
		}
	}
	filteredGC := make([]gcRow, 0, len(gameCubeGames))
	totalDiscs := 0
	for _, game := range gameCubeGames {
		totalDiscs += len(game.Discs)
		if matches(game.Title, game.ID) {
			filteredGC = append(filteredGC, gcRow{
				Game: game, Included: included[fmt.Sprintf("%s:%d", game.ID, game.Revision)],
			})
		}
	}
	type reviewRow struct{ Platform, Path, Reason string }
	displayPath := func(root, path string) string {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != "." && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return relative
		}
		return filepath.Base(path)
	}
	review := make([]reviewRow, 0, len(wiiRejections)+len(gameCubeRejections))
	for _, item := range wiiRejections {
		review = append(review, reviewRow{"Wii", displayPath(libraryRoot, item.Path), item.Reason})
	}
	for _, item := range gameCubeRejections {
		review = append(review, reviewRow{"GameCube", displayPath(a.libraryRoot("gamecube"), item.Path), item.Reason})
	}
	sort.Slice(review, func(i, j int) bool {
		if review[i].Platform == review[j].Platform {
			return review[i].Path < review[j].Path
		}
		return review[i].Platform < review[j].Platform
	})
	filter := r.URL.Query().Get("platform")
	if filter != "wii" && filter != "gamecube" {
		filter = "all"
	}
	csrf := a.csrf
	if session, ok := browserSession(r); ok {
		csrf = session.CSRF
	}
	piAddress := ""
	piSwitchReady := true
	if a.pi != nil {
		piAddress = a.pi.Address()
		status, statusErr := a.pi.Status(r.Context())
		piSwitchReady = statusErr == nil && validatePiSwitchReadiness(status) == nil
	}
	storageControls := []map[string]string{
		{"Action": "detach", "Label": "Safely Detach USB"},
		{"Action": "disconnect", "Label": "Disconnect NBD"},
		{"Action": "connect", "Label": "Connect Current Library"},
		{"Action": "attach", "Label": "Attach USB"},
		{"Action": "reconcile", "Label": "Reconcile Connection"},
	}
	generation := ""
	if activeReady {
		generation = active.GenerationID
	} else if managed, managedErr := a.gcLibrary.ManagedActive(); managedErr == nil {
		generation = managed.GenerationID
	}
	compatibilityState := compatibility.Status
	if compatibilityState == "" {
		compatibilityState = compat.StateUnknown
	}
	data := map[string]any{
		"Version": version, "Wii": filteredWii, "GameCube": filteredGC,
		"Filter": filter, "Query": query, "CSRF": csrf,
		"Platform": a.exports.Platform(), "State": a.exports.State(),
		"Notice":   strings.TrimSpace(r.URL.Query().Get("notice")),
		"TotalWii": len(wiiGames), "TotalGameCube": len(gameCubeGames),
		"GameCubeDiscs": totalDiscs, "GameCubeMode": strings.Title(string(a.gcMode)),
		"GameCubeModeRaw": string(a.gcMode), "SaveSelection": saveSelection,
		"SaveBackupInterval": (time.Duration(saveSelection.AutomaticBackupInterval) * time.Second).String(),
		"SaveError":          saveError,
		"GCBuild":            a.gcLibrary.Progress(), "GCReady": activeReady,
		"GCGeneration": generation, "GCUpdate": gcUpdate,
		"GCLegacy": len(a.gcLibrary.LegacyGenerations()) > 0,
		"Rejected": len(review), "Review": review,
		"AutomaticSwitch": a.pi != nil, "PiAddress": piAddress,
		"PiSwitchReady":   piSwitchReady,
		"StorageControls": storageControls,
		"DefaultPassword": a.browser.DefaultActive(),
		"Source":          sourceRecord, "LibrarySources": []map[string]any{
			{"Platform": "wii", "Label": "Wii", "Source": sourceRecord},
			{"Platform": "gamecube", "Label": "GameCube", "Source": gcSourceRecord},
		}, "Compatibility": map[string]any{"Status": compatibilityState},
		"HostRevision":       gitCommit,
		"HostRevisionShort":  displayRevision(gitCommit),
		"HostProtocol":       fmt.Sprintf("%d–%d", compat.ProtocolMin, compat.ProtocolMax),
		"DashboardRefreshMS": max(a.dashboardRefresh.Milliseconds(), int64(2000)),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.web.Execute(w, "dashboard.html", data); err != nil {
		slog.Warn("dashboard render failed", "error", err)
	}
}

func humanBytes(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exponent := unit, 0
	for value := size / unit; value >= unit && exponent < 4; value /= unit {
		div *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exponent])
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respondAction(w http.ResponseWriter, r *http.Request, status int, value any, notice, platform string) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		query := url.Values{"notice": {notice}, "platform": {platform}}
		http.Redirect(w, r, "/?"+query.Encode(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
