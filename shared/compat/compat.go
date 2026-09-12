// Package compat owns the versioned Host/firmware compatibility contract.
package compat

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"wiibridge/shared/contract"
)

const (
	SchemaVersion = 1
	ProtocolMin   = 1
	ProtocolMax   = 1

	CapWiiReadOnly                = "wii-read-only-export-v1"
	CapGameCubeSchema2            = "gamecube-schema2-no-copy-v1"
	CapGameCubePhysicalMemoryCard = "gamecube-physical-memory-card-v1"
	CapGameCubeSaveOverlay        = "gamecube-save-overlay-v1"
	CapSafePlatformSwitching      = "safe-platform-switching-v1"
	CapUSBDetach                  = "usb-detach-v1"
	CapUSBAttach                  = "usb-attach-v1"
	CapNBDConnect                 = "nbd-connect-v1"
	CapNBDDisconnect              = "nbd-disconnect-v1"
	CapAutomaticPlatformSwitching = "automatic-platform-switching-v1"
	CapStartupReadiness           = "startup-readiness-v1"
	CapFirmwareReboot             = "firmware-reboot-v1"
	CapFirmwareShutdown           = "firmware-shutdown-v1"
	CapDiagnosticStatus           = "diagnostic-status-v1"
	CapRuntimeMetrics             = "runtime-metrics-v1"
	CapSourceOfflineStatus        = "source-offline-status-v1"
	CapSeparateLibraryPaths       = "separate-library-paths-v1"
	CapWiiFAT32ExactFSInfoSplit   = "wii-fat32-exact-fsinfo-split-v1"
)

var capabilityPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*-v[1-9][0-9]*$`)

type Descriptor struct {
	SchemaVersion  int      `json:"schemaVersion"`
	ProtocolMin    int      `json:"protocolMin"`
	ProtocolMax    int      `json:"protocolMax"`
	ProductVersion string   `json:"productVersion"`
	Revision       string   `json:"revision"`
	BuildTime      string   `json:"buildTime"`
	BuildDirty     bool     `json:"buildDirty"`
	Platform       string   `json:"platform"`
	Board          string   `json:"board"`
	DeviceID       string   `json:"deviceId,omitempty"`
	Capabilities   []string `json:"capabilities"`
}

func (d Descriptor) Validate(expectedPlatform string) error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported compatibility descriptor schema %d", d.SchemaVersion)
	}
	if d.ProtocolMin < 1 || d.ProtocolMax < d.ProtocolMin {
		return errors.New("invalid compatibility protocol range")
	}
	if d.ProductVersion == "" || len(d.ProductVersion) > 64 ||
		d.Revision == "" || len(d.Revision) > 128 ||
		d.BuildTime == "" || len(d.BuildTime) > 64 {
		return errors.New("incomplete compatibility build identity")
	}
	if d.BuildTime != "unknown" {
		if _, err := time.Parse(time.RFC3339, d.BuildTime); err != nil {
			return errors.New("compatibility build time is not RFC3339")
		}
	}
	if expectedPlatform != "" && d.Platform != expectedPlatform {
		return errors.New("unexpected compatibility descriptor platform")
	}
	if d.Platform != "host" && d.Platform != "firmware" {
		return errors.New("invalid compatibility descriptor platform")
	}
	if d.Platform == "firmware" && (d.Board == "" || len(d.Board) > 128) {
		return errors.New("firmware descriptor has no board identity")
	}
	seen := make(map[string]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if !capabilityPattern.MatchString(capability) {
			return errors.New("malformed compatibility capability")
		}
		if _, duplicate := seen[capability]; duplicate {
			return errors.New("duplicate compatibility capability")
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func NewDescriptor(platform, board, deviceID, productVersion, revision, buildTime string,
	capabilities []string,
) Descriptor {
	copyCapabilities := append([]string(nil), capabilities...)
	sort.Strings(copyCapabilities)
	return Descriptor{
		SchemaVersion: SchemaVersion, ProtocolMin: ProtocolMin, ProtocolMax: ProtocolMax,
		ProductVersion: productVersion, Revision: revision, BuildTime: buildTime,
		Platform: platform, Board: board, DeviceID: deviceID, Capabilities: copyCapabilities,
	}
}

func HostCapabilities() []string {
	return []string{
		CapDiagnosticStatus, CapGameCubePhysicalMemoryCard, CapGameCubeSaveOverlay,
		CapGameCubeSchema2, CapRuntimeMetrics, CapSourceOfflineStatus, CapSeparateLibraryPaths,
		CapStartupReadiness, CapWiiFAT32ExactFSInfoSplit, CapWiiReadOnly,
	}
}

func FirmwareCapabilities() []string {
	return []string{
		CapAutomaticPlatformSwitching, CapDiagnosticStatus, CapFirmwareReboot,
		CapFirmwareShutdown, CapGameCubePhysicalMemoryCard, CapGameCubeSaveOverlay,
		CapGameCubeSchema2, CapNBDConnect, CapNBDDisconnect, CapRuntimeMetrics,
		CapSafePlatformSwitching, CapUSBAttach, CapUSBDetach, CapWiiReadOnly,
	}
}

type State string

const (
	StateCompatible             State = "compatible"
	StateCompatibleWithWarnings State = "compatible-with-warnings"
	StateBlocked                State = "blocked"
	StateUnreachable            State = "unreachable"
	StateUnknown                State = "unknown"
)

type Operation string

const (
	OperationStatus             Operation = "status"
	OperationWiiConnect         Operation = "wii-connect"
	OperationGameCubePhysical   Operation = "gamecube-physical"
	OperationGameCubeEmulated   Operation = "gamecube-emulated"
	OperationUSBDetach          Operation = "usb-detach"
	OperationUSBAttach          Operation = "usb-attach"
	OperationNBDDisconnect      Operation = "nbd-disconnect"
	OperationSafeDisconnect     Operation = "safe-disconnect"
	OperationAutomaticSwitch    Operation = "automatic-switch"
	OperationReboot             Operation = "reboot"
	OperationShutdown           Operation = "shutdown"
	OperationPerformanceMetrics Operation = "performance-metrics"
)

type Requirements struct {
	Required []string
	Optional []string
}

func RequirementsFor(operation Operation) Requirements {
	switch operation {
	case OperationWiiConnect:
		return Requirements{Required: []string{CapWiiReadOnly, CapNBDConnect, CapUSBAttach}}
	case OperationGameCubePhysical:
		return Requirements{Required: []string{
			CapGameCubeSchema2, CapGameCubePhysicalMemoryCard,
			CapSafePlatformSwitching, CapNBDConnect, CapUSBAttach,
		}}
	case OperationGameCubeEmulated:
		return Requirements{Required: []string{
			CapGameCubeSchema2, CapGameCubeSaveOverlay, CapSafePlatformSwitching,
			CapNBDConnect, CapUSBAttach,
		}}
	case OperationUSBDetach:
		return Requirements{Required: []string{CapUSBDetach}}
	case OperationUSBAttach:
		return Requirements{Required: []string{CapUSBAttach}}
	case OperationNBDDisconnect:
		return Requirements{Required: []string{CapNBDDisconnect}}
	case OperationSafeDisconnect:
		return Requirements{Required: []string{CapUSBDetach, CapNBDDisconnect}}
	case OperationAutomaticSwitch:
		return Requirements{Required: []string{
			CapAutomaticPlatformSwitching, CapSafePlatformSwitching, CapUSBDetach,
			CapNBDDisconnect, CapNBDConnect, CapUSBAttach,
		}}
	case OperationReboot:
		return Requirements{Required: []string{CapFirmwareReboot}}
	case OperationShutdown:
		return Requirements{Required: []string{CapFirmwareShutdown}}
	case OperationPerformanceMetrics:
		return Requirements{Optional: []string{CapRuntimeMetrics}}
	default:
		return Requirements{
			Required: []string{CapDiagnosticStatus},
			Optional: []string{CapRuntimeMetrics},
		}
	}
}

type Result struct {
	Status               State            `json:"status"`
	CheckedAt            time.Time        `json:"checkedAt"`
	Host                 Descriptor       `json:"host"`
	Firmware             Descriptor       `json:"firmware"`
	NegotiatedProtocol   *int             `json:"negotiatedProtocol"`
	Operation            Operation        `json:"operation"`
	RequiredCapabilities []string         `json:"requiredCapabilities"`
	MissingCapabilities  []string         `json:"missingCapabilities"`
	Warnings             []contract.Error `json:"warnings"`
	Errors               []contract.Error `json:"errors"`
}

type EvaluateOptions struct {
	Operation          Operation
	ExpectedBoard      string
	ExpectedDeviceID   string
	BoardSupported     *bool
	AdditionalRequired []string
	Now                time.Time
}

func Evaluate(host, firmware Descriptor, options EvaluateOptions) Result {
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	requirements := RequirementsFor(options.Operation)
	requirements.Required = append(
		append([]string(nil), requirements.Required...), options.AdditionalRequired...)
	result := Result{
		Status: StateCompatible, CheckedAt: options.Now, Host: host, Firmware: firmware,
		Operation:            options.Operation,
		RequiredCapabilities: append([]string(nil), requirements.Required...),
	}
	sort.Strings(result.RequiredCapabilities)
	if err := host.Validate("host"); err != nil {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-DESCRIPTOR-MALFORMED", "host", err.Error(), false))
		return result
	}
	if err := firmware.Validate("firmware"); err != nil {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-DESCRIPTOR-MALFORMED", "firmware", err.Error(), false))
		return result
	}
	if options.ExpectedBoard != "" && firmware.Board != options.ExpectedBoard {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-DEVICE-IDENTITY-MISMATCH", "firmware",
			"Connected firmware board identity does not match the configured device.", false))
	}
	if options.BoardSupported != nil && !*options.BoardSupported {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-BOARD-UNSUPPORTED", "firmware",
			"The connected firmware reports an unsupported board.", false))
	}
	if options.ExpectedDeviceID != "" && firmware.DeviceID != options.ExpectedDeviceID {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-DEVICE-IDENTITY-MISMATCH", "firmware",
			"Connected firmware device identity does not match the pinned device.", false))
	}
	minimum := max(host.ProtocolMin, firmware.ProtocolMin)
	maximum := min(host.ProtocolMax, firmware.ProtocolMax)
	if minimum > maximum {
		result.Status = StateBlocked
		result.Errors = append(result.Errors, compatError(
			"COMPAT-PROTOCOL-NO-OVERLAP", "protocol",
			"Host and firmware protocol ranges do not overlap.", false))
	} else {
		protocol := maximum
		result.NegotiatedProtocol = &protocol
	}
	available := make(map[string]struct{}, len(firmware.Capabilities))
	for _, capability := range firmware.Capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range requirements.Required {
		if _, ok := available[capability]; !ok {
			result.MissingCapabilities = append(result.MissingCapabilities, capability)
		}
	}
	sort.Strings(result.MissingCapabilities)
	if len(result.MissingCapabilities) > 0 {
		result.Status = StateBlocked
		message := "Connected firmware is missing required capability: " +
			strings.Join(result.MissingCapabilities, ", ") + "."
		result.Errors = append(result.Errors, compatError(
			"COMPAT-CAPABILITY-MISSING", "firmware", message, false))
	}
	for _, capability := range requirements.Optional {
		if _, ok := available[capability]; !ok {
			result.Warnings = append(result.Warnings, compatError(
				"COMPAT-CAPABILITY-MISSING", "firmware",
				"Optional firmware capability is unavailable: "+capability+".", true))
		}
	}
	if result.Status == StateCompatible && len(result.Warnings) > 0 {
		result.Status = StateCompatibleWithWarnings
	}
	return result
}

func MarkCachedStale(result Result, now time.Time, maximumAge time.Duration) Result {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if maximumAge <= 0 {
		maximumAge = 30 * time.Second
	}
	if result.CheckedAt.IsZero() || now.Sub(result.CheckedAt) <= maximumAge {
		return result
	}
	result.Warnings = append(result.Warnings, contract.New(
		"COMPAT-STATUS-STALE", "compatibility", contract.SeverityWarning,
		"The displayed compatibility result is stale and cannot authorize an operation.", true))
	if result.Status == StateCompatible {
		result.Status = StateCompatibleWithWarnings
	}
	return result
}

func Unreachable(host Descriptor, operation Operation, message string) Result {
	result := Result{
		Status: StateUnreachable, CheckedAt: time.Now().UTC(), Host: host,
		Operation: operation,
	}
	result.Errors = append(result.Errors, compatError(
		"COMPAT-FIRMWARE-UNREACHABLE", "firmware", message, true))
	return result
}

func compatError(code, component, message string, retryable bool) contract.Error {
	return contract.New(code, component, contract.SeverityError, message, retryable)
}
