package main

import (
	"context"
	"log/slog"
	"time"

	"wiibridge/shared/sourcehealth"
)

func retryableSource(state sourcehealth.State) bool {
	switch state {
	case sourcehealth.StateOffline, sourcehealth.StateUnreachable,
		sourcehealth.StateMountMissing, sourcehealth.StateTemporaryUnavailable,
		sourcehealth.StatePermissionDenied:
		return true
	}
	return false
}

// Probe metadata with capped backoff. A scan is attempted only after the saved
// identity matches again. Never accept a replacement or confirm missing items
// merely because a timer fired. Manual scans and startup share the same lock.
func (a *app) runSourceRecovery(ctx context.Context) {
	delay := 5 * time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		a.recoverSources(ctx, assertReadOnly)
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
}

func (a *app) recoverSources(ctx context.Context, verifyReadOnly func(string) error) {
	groups := [][]string{{"wii", "gamecube"}}
	if a.root != a.libraryRoot("gamecube") {
		groups = [][]string{{"wii"}, {"gamecube"}}
	}
	for _, group := range groups {
		if ctx.Err() != nil || !a.scanMu.TryLock() {
			return
		}
		a.mu.RLock()
		previous := a.librarySourceLocked(group[0])
		for _, platform := range group {
			if candidate := a.librarySourceLocked(platform); retryableSource(candidate.State) {
				previous = candidate
				break
			}
		}
		a.mu.RUnlock()
		if !retryableSource(previous.State) {
			a.scanMu.Unlock()
			continue
		}
		if _, err := sourcehealth.Preflight(a.libraryRoot(group[0]), &previous); err != nil {
			a.scanMu.Unlock()
			continue
		}
		err := a.scanLibraryGroup(group, false, verifyReadOnly)
		a.scanMu.Unlock()
		if err != nil {
			continue
		}
		slog.Info("Library source recovered after validation", "platform", group[0])
		for _, platform := range group {
			if platform == "gamecube" && a.gcLibrary != nil {
				progress := a.gcLibrary.Progress()
				if progress.Validation == "pending" {
					_ = a.gcLibrary.StartActiveValidation(ctx)
				}
			}
		}
	}
}
