// Package sourcehealth models source-root availability separately from item
// existence. It never writes probes into a source library.
package sourcehealth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wiibridge/shared/sourceidentity"
)

type State string

const (
	StateAvailable            State = "available"
	StateOffline              State = "offline"
	StateUnreachable          State = "unreachable"
	StatePermissionDenied     State = "permission-denied"
	StateAuthenticationFailed State = "authentication-failed"
	StateMountMissing         State = "mount-missing"
	StateTemporaryUnavailable State = "temporarily-unavailable"
	StateChanged              State = "changed"
	StateMissingConfirmed     State = "missing-confirmed"
	StateInvalid              State = "invalid"
	StateUnknown              State = "unknown"
)

type Availability string

const (
	AvailabilityPlayable           Availability = "playable"
	AvailabilitySourceOffline      Availability = "source-offline"
	AvailabilitySourceChanged      Availability = "source-changed"
	AvailabilityValidationRequired Availability = "validation-required"
	AvailabilityMissingConfirmed   Availability = "missing-confirmed"
	AvailabilityInvalid            Availability = "invalid"
)

type Record struct {
	SourceID                string    `json:"source_id"`
	RootPath                string    `json:"configured_root_path"`
	State                   State     `json:"state"`
	LastSuccessfulScan      time.Time `json:"last_successful_scan,omitempty"`
	LastAttemptedScan       time.Time `json:"last_attempted_scan"`
	LastSuccessfulItemCount int       `json:"last_successful_item_count"`
	FailureCode             string    `json:"failure_code,omitempty"`
	FailureMessage          string    `json:"failure_message,omitempty"`
	ConsecutiveFailures     int       `json:"consecutive_failure_count"`
	FilesystemID            string    `json:"filesystem_id,omitempty"`
	RootInode               uint64    `json:"root_inode,omitempty"`
	LastKnownDevice         uint64    `json:"last_known_device,omitempty"`
	LastKnownFilesystem     string    `json:"last_known_filesystem,omitempty"`
	LastKnownMountInfo      string    `json:"last_known_mount_information,omitempty"`
}

type PreflightResult struct {
	Record Record `json:"source"`
	Code   string `json:"code,omitempty"`
}

func Preflight(root string, previous *Record) (PreflightResult, error) {
	return preflight(root, previous, sourceidentity.FilesystemID)
}

func preflight(root string, previous *Record, filesystemID func(string) (string, error)) (PreflightResult, error) {
	now := time.Now().UTC()
	absolute, err := filepath.Abs(root)
	if err != nil {
		return failure(previous, absolute, StateInvalid, "SOURCE-OFFLINE",
			"Configured source path is invalid.", now, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return classifyFailure(previous, absolute, now, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return failure(previous, absolute, StateInvalid, "SOURCE-OFFLINE",
			"Configured source is not a regular directory.", now, errors.New("invalid source root"))
	}
	device, inode := uint64(0), uint64(0)
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		device, inode = uint64(stat.Dev), stat.Ino
	}
	stableID, err := filesystemID(absolute)
	if err != nil {
		return classifyFailure(previous, absolute, now, err)
	}
	filesystem, mount := mountIdentity(absolute)
	stableBaseline := previous != nil && previous.FilesystemID != ""
	if stableBaseline && (stableID != previous.FilesystemID || inode == 0 || inode != previous.RootInode || absolute != previous.RootPath) {
		return failure(previous, absolute, StateChanged, "SOURCE-IDENTITY-CHANGED",
			"The source filesystem or library directory changed; confirm the intended location.", now,
			errors.New("source filesystem or directory changed"))
	}
	if !stableBaseline && previous != nil && previous.LastKnownMountInfo != "" &&
		mount != previous.LastKnownMountInfo {
		return failure(previous, absolute, StateMountMissing, "SOURCE-MOUNT-MISSING",
			"The configured source mount was replaced or is no longer mounted.", now,
			errors.New("source mount missing"))
	}
	if !stableBaseline && previous != nil && previous.LastKnownDevice != 0 && device != 0 &&
		previous.LastKnownDevice != device {
		return failure(previous, absolute, StateChanged, "SOURCE-IDENTITY-CHANGED",
			"Source root identity changed; validation is required.", now,
			errors.New("source device changed"))
	}
	directory, err := os.Open(absolute)
	if err != nil {
		return classifyFailure(previous, absolute, now, err)
	}
	names, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return classifyFailure(previous, absolute, now, readErr)
	}
	if closeErr != nil {
		return classifyFailure(previous, absolute, now, closeErr)
	}
	if len(names) == 0 && errors.Is(readErr, io.EOF) && previous != nil &&
		previous.LastSuccessfulItemCount > 0 {
		return failure(previous, absolute, StateMountMissing, "SOURCE-MOUNT-MISSING",
			"The source root is unexpectedly empty; the prior catalog was preserved.",
			now, errors.New("source root unexpectedly empty"))
	}
	record := base(previous, absolute, now)
	record.SourceID = sourceID(absolute, device, mount)
	if stableID != "" && inode != 0 {
		record.FilesystemID, record.RootInode = stableID, inode
		record.SourceID = sourceID(absolute, inode, stableID)
	}
	record.State = StateAvailable
	record.FailureCode, record.FailureMessage, record.ConsecutiveFailures = "", "", 0
	record.LastKnownDevice, record.LastKnownFilesystem = device, filesystem
	record.LastKnownMountInfo = mount
	return PreflightResult{Record: record}, nil
}

// PreflightReplacement inspects an explicitly selected replacement mount. Its
// identity is only a candidate: callers must complete discovery and validation
// before committing it. Failed recovery retains the last trusted identity.
func PreflightReplacement(root string, previous Record) (PreflightResult, error) {
	baseline := previous
	baseline.LastKnownDevice, baseline.LastKnownMountInfo = 0, ""
	baseline.FilesystemID, baseline.RootInode = "", 0
	result, err := Preflight(root, &baseline)
	if err != nil {
		return failure(&previous, result.Record.RootPath, result.Record.State,
			result.Record.FailureCode, result.Record.FailureMessage,
			result.Record.LastAttemptedScan, err)
	}
	return result, nil
}

func Successful(previous Record, itemCount int) Record {
	previous.State = StateAvailable
	previous.LastSuccessfulScan = time.Now().UTC()
	previous.LastAttemptedScan = previous.LastSuccessfulScan
	previous.LastSuccessfulItemCount = itemCount
	previous.FailureCode, previous.FailureMessage = "", ""
	previous.ConsecutiveFailures = 0
	return previous
}

func Partial(previous Record, cause error) Record {
	result, _ := failure(&previous, previous.RootPath, StateTemporaryUnavailable,
		"SOURCE-PARTIAL-SCAN", "Source scan stopped before all items were visited.",
		time.Now().UTC(), cause)
	return result.Record
}

func RuntimeFailure(previous Record, code string) Record {
	state := StateTemporaryUnavailable
	message := "A source read failed while serving an active export."
	switch code {
	case "SOURCE-IDENTITY-CHANGED":
		state = StateChanged
		message = "A source identity changed while an active generation was in use."
	case "SOURCE-PERMISSION-DENIED":
		state = StatePermissionDenied
		message = "Source access was denied while serving an active export."
	case "SOURCE-READONLY-GUARANTEE-FAILED":
		state = StateInvalid
		message = "The configured source is not mounted read-only."
	default:
		code = "SOURCE-READ-FAILED"
	}
	result, _ := failure(&previous, previous.RootPath, state, code, message,
		time.Now().UTC(), errors.New("runtime source failure"))
	return result.Record
}

func DerivedAvailability(source State, item Availability) Availability {
	switch source {
	case StateAvailable:
		if item == "" {
			return AvailabilityPlayable
		}
		return item
	case StateChanged:
		return AvailabilitySourceChanged
	case StateInvalid:
		return AvailabilityInvalid
	default:
		return AvailabilitySourceOffline
	}
}

func classifyFailure(previous *Record, root string, now time.Time, cause error) (PreflightResult, error) {
	state, code, message := StateOffline, "SOURCE-OFFLINE", "Source storage is offline."
	switch {
	case errors.Is(cause, os.ErrPermission):
		state, code, message = StatePermissionDenied, "SOURCE-PERMISSION-DENIED",
			"Source storage cannot be read with the service identity."
	case errors.Is(cause, os.ErrNotExist):
		state, code, message = StateMountMissing, "SOURCE-MOUNT-MISSING",
			"Configured source mount is missing."
	case errors.Is(cause, syscall.ETIMEDOUT), errors.Is(cause, syscall.ENETUNREACH),
		errors.Is(cause, syscall.EHOSTUNREACH):
		state, code, message = StateUnreachable, "SOURCE-UNREACHABLE",
			"Source storage is temporarily unreachable."
	case errors.Is(cause, syscall.EIO), errors.Is(cause, syscall.ESTALE):
		state, code, message = StateTemporaryUnavailable, "SOURCE-OFFLINE",
			"Source storage returned a temporary I/O failure."
	}
	return failure(previous, root, state, code, message, now, cause)
}

func failure(previous *Record, root string, state State, code, message string,
	now time.Time, cause error,
) (PreflightResult, error) {
	record := base(previous, root, now)
	record.State, record.FailureCode, record.FailureMessage = state, code, message
	record.ConsecutiveFailures++
	return PreflightResult{Record: record, Code: code}, cause
}

func base(previous *Record, root string, now time.Time) Record {
	if previous != nil {
		result := *previous
		result.RootPath, result.LastAttemptedScan = root, now
		return result
	}
	return Record{
		SourceID: sourceID(root, 0, ""), RootPath: root, State: StateUnknown,
		LastAttemptedScan: now,
	}
}

func sourceID(root string, device uint64, mount string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + strconv.FormatUint(device, 10) + "\x00" + mount))
	return "source-" + hex.EncodeToString(sum[:12])
}

func mountIdentity(root string) (string, string) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", ""
	}
	root = filepath.Clean(root)
	bestMount, bestFS, bestIdentity := "", "", ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || separator+2 >= len(fields) {
			continue
		}
		mountPoint := unescapeMount(fields[4])
		if root != mountPoint && !strings.HasPrefix(root, strings.TrimSuffix(mountPoint, string(os.PathSeparator))+string(os.PathSeparator)) {
			continue
		}
		if len(mountPoint) < len(bestMount) {
			continue
		}
		bestMount, bestFS = mountPoint, fields[separator+1]
		// Store stable non-secret mount identity. The backing source field can
		// contain administrator paths or credentials and is intentionally not
		// persisted.
		bestIdentity = fields[2] + ":" + fields[3] + ":" + mountPoint
	}
	return bestFS, bestIdentity
}

func unescapeMount(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`,
	)
	return replacer.Replace(value)
}
