package sourcehealth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementPreflightRetainsTrustedIdentityOnFailure(t *testing.T) {
	root := t.TempDir()
	initial, err := Preflight(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := Successful(initial.Record, 42)
	previous.LastKnownDevice = 99
	previous.LastKnownMountInfo = "synthetic:previous:mount"
	for _, path := range []string{root, filepath.Join(root, "missing")} {
		candidate, candidateErr := PreflightReplacement(path, previous)
		if candidateErr == nil || candidate.Record.LastKnownDevice != previous.LastKnownDevice || candidate.Record.LastKnownMountInfo != previous.LastKnownMountInfo || candidate.Record.LastSuccessfulItemCount != 42 {
			t.Fatalf("failed replacement discarded trust baseline: %#v %v", candidate, candidateErr)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "entry"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := PreflightReplacement(root, previous)
	if err != nil || candidate.Record.State != StateAvailable || candidate.Record.LastKnownMountInfo == previous.LastKnownMountInfo || candidate.Record.LastKnownDevice == previous.LastKnownDevice {
		t.Fatalf("replacement mount not inspected: %#v %v", candidate, err)
	}
}

func TestPreflightAvailableAndMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Preflight(root, nil)
	if err != nil || result.Record.State != StateAvailable ||
		result.Record.SourceID == "" {
		t.Fatalf("available=%#v err=%v", result, err)
	}
	missing := filepath.Join(root, "missing")
	result, err = Preflight(missing, &result.Record)
	if err == nil || result.Record.State != StateMountMissing ||
		result.Record.FailureCode != "SOURCE-MOUNT-MISSING" ||
		result.Record.LastSuccessfulItemCount != 0 {
		t.Fatalf("missing=%#v err=%v", result, err)
	}
}

func TestChangedIdentityAndDerivedAvailability(t *testing.T) {
	root := t.TempDir()
	result, err := Preflight(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := Successful(result.Record, 10)
	previous.FilesystemID, previous.RootInode = "", 0
	previous.LastKnownDevice++
	changed, err := Preflight(root, &previous)
	if err == nil || changed.Record.State != StateChanged {
		t.Fatalf("changed=%#v err=%v", changed, err)
	}
	if got := DerivedAvailability(StateOffline, AvailabilityPlayable); got != AvailabilitySourceOffline {
		t.Fatalf("availability=%s", got)
	}
	if got := DerivedAvailability(StateAvailable, AvailabilityMissingConfirmed); got != AvailabilityMissingConfirmed {
		t.Fatalf("available state=%s", got)
	}
}

func TestPartialPreservesLastSuccessfulMetadata(t *testing.T) {
	record := Successful(Record{RootPath: "/library"}, 42)
	partial := Partial(record, os.ErrPermission)
	if partial.LastSuccessfulItemCount != 42 ||
		partial.FailureCode != "SOURCE-PARTIAL-SCAN" ||
		partial.State != StateTemporaryUnavailable {
		t.Fatalf("partial=%#v", partial)
	}
}

func TestEmptyMountpointReplacementIsNotADeletion(t *testing.T) {
	root := t.TempDir()
	available, err := Preflight(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := Successful(available.Record, 17)
	previous.FilesystemID, previous.RootInode = "", 0
	previous.LastKnownMountInfo = "synthetic:missing:mount"
	replaced, err := Preflight(root, &previous)
	if err == nil || replaced.Record.State != StateMountMissing ||
		replaced.Record.FailureCode != "SOURCE-MOUNT-MISSING" ||
		replaced.Record.LastSuccessfulItemCount != 17 {
		t.Fatalf("replacement=%#v err=%v", replaced, err)
	}
}

func TestUnexpectedlyEmptySourcePreservesPriorCount(t *testing.T) {
	root := t.TempDir()
	available, err := Preflight(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := Successful(available.Record, 17)
	empty, err := Preflight(root, &previous)
	if err == nil || empty.Record.State != StateMountMissing ||
		empty.Record.FailureCode != "SOURCE-MOUNT-MISSING" ||
		empty.Record.LastSuccessfulItemCount != 17 {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
}

func TestRuntimeFailurePreservesSuccessfulSnapshot(t *testing.T) {
	previous := Successful(Record{
		SourceID: "source-test", RootPath: "/library", State: StateAvailable,
	}, 8)
	failed := RuntimeFailure(previous, "SOURCE-IDENTITY-CHANGED")
	if failed.State != StateChanged ||
		failed.FailureCode != "SOURCE-IDENTITY-CHANGED" ||
		failed.LastSuccessfulItemCount != 8 ||
		failed.LastSuccessfulScan.IsZero() {
		t.Fatalf("runtime failure=%#v", failed)
	}
}
