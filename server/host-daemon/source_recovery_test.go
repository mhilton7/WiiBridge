package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"wiibridge/shared/sourcehealth"
)

func TestAutomaticRecoveryRetainsCatalogUntilOriginalDirectoryReturns(t *testing.T) {
	a := separateLibraryApp(t)
	original := a.root + "-offline"
	if err := os.Rename(a.root, original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(original, a.root) })
	if err := a.scanLibraryGroup([]string{"wii"}, false, syntheticReadOnly); err == nil {
		t.Fatal("missing directory accepted")
	}
	a.recoverSources(context.Background(), syntheticReadOnly)
	if a.ready || len(a.scan.Games) != 1 || a.source.State != sourcehealth.StateMountMissing {
		t.Fatal("missing source changed catalog/readiness")
	}
	if err := os.Rename(original, a.root); err != nil {
		t.Fatal(err)
	}
	a.recoverSources(context.Background(), syntheticReadOnly)
	if !a.ready || a.source.State != sourcehealth.StateAvailable || len(a.scan.Games) != 1 || a.scan.Games[0].Availability != "playable" {
		t.Fatal("returning source did not automatically recover")
	}
	if a.gcSource.State != sourcehealth.StateAvailable {
		t.Fatal("recovery disturbed other library")
	}
}

func TestAutomaticRecoveryRejectsEmptyOrDifferentSourceAndCancellation(t *testing.T) {
	a := separateLibraryApp(t)
	if err := os.Remove(filepath.Join(a.root, "wii.wbfs")); err != nil {
		t.Fatal(err)
	}
	a.source.State, a.ready = sourcehealth.StateMountMissing, false
	a.recoverSources(context.Background(), syntheticReadOnly)
	if a.ready || len(a.scan.Games) != 1 {
		t.Fatal("empty source replaced prior catalog")
	}
	a.source.State = sourcehealth.StateChanged
	a.recoverSources(context.Background(), func(string) error { t.Fatal("changed source retried without confirmation"); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.recoverSources(ctx, func(string) error { t.Fatal("canceled recovery performed scan"); return nil })
}
