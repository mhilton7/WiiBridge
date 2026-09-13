package gamecube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type cancelingReader struct {
	cancel  context.CancelFunc
	calls   int
	maxRead int
}

func (reader *cancelingReader) Read(buffer []byte) (int, error) {
	reader.calls++
	reader.maxRead = max(reader.maxRead, len(buffer))
	clear(buffer)
	reader.cancel()
	return len(buffer), nil
}

func TestHashReadCancellationIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelingReader{cancel: cancel}
	_, err := io.Copy(io.Discard, contextReader{ctx: ctx, reader: source})
	if !errors.Is(err, context.Canceled) || source.calls != 1 || source.maxRead > 32<<10 {
		t.Fatalf("cancellation err=%v reads=%d max=%d", err, source.calls, source.maxRead)
	}
}

func TestContextHashPreservesFingerprintAndCallerDiscs(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte("synthetic hash bytes"), 10000)
	path := filepath.Join(root, "disc.iso")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	game := Game{Discs: []Disc{{SourcePath: path, Format: "iso"}}}
	hashed, err := hashGameSourcesContext(context.Background(), game)
	if err != nil || hashed.Discs[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint changed: %v", err)
	}
	if game.Discs[0].SHA256 != "" {
		t.Fatal("fingerprinting mutated the caller's disc slice")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = hashFileContext(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("file cancellation=%v", err)
	}
	if _, _, err = hashTreeContext(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("tree cancellation=%v", err)
	}
}

func TestCancelOnLastValidationProgressIsNotSuccess(t *testing.T) {
	sources, managed := t.TempDir(), t.TempDir()
	manager := libraryManager(t, managed, sources)
	manifest, err := manager.Build(context.Background(), libraryGames(t, sources))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = ValidateLibraryManifestDeep(ctx, manager.Root(), manifest, func(update ValidationProgress) {
		if update.FilesCompleted == update.TotalFiles {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("last-file cancellation returned %v", err)
	}
}
