package gamecube

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"testing"
)

func TestSaveReadsAcrossCleanAndDirtySpans(t *testing.T) {
	root := t.TempDir()
	object := testSaveObject(t, root)
	store := openTestSaveStore(t, root, object)
	defer store.Close()
	expected, err := os.ReadFile(store.objects[object.ID].file.Name())
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		random := rand.New(rand.NewSource(42))
		for index := 0; index < 200; index++ {
			offset := random.Intn(len(expected))
			length := random.Intn(len(expected) - offset + 1)
			actual := make([]byte, length)
			if n, err := store.ReadSaveAt(object.ID, actual, int64(offset)); err != nil ||
				n != length || !bytes.Equal(actual, expected[offset:offset+length]) {
				t.Fatalf("read offset=%d length=%d n=%d err=%v", offset, length, n, err)
			}
		}
	}
	check()
	for _, span := range [][2]int{{511, 3}, {4097, 1023}, {32767, 32770}, {len(expected) - 1, 1}} {
		data := bytes.Repeat([]byte{byte(span[0])}, span[1])
		if n, err := store.WriteSaveAt(object.ID, data, int64(span[0])); err != nil || n != len(data) {
			t.Fatalf("write=%d err=%v", n, err)
		}
		copy(expected[span[0]:], data)
	}
	check()
	if err := store.Sync(); err != nil {
		t.Fatal(err)
	}
	check()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSaveAt(object.ID, make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed read error=%v", err)
	}
}

func TestSaveCleanSpanShortRead(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "mixed"}[mixed], func(t *testing.T) {
			root := t.TempDir()
			object := testSaveObject(t, root)
			store := openTestSaveStore(t, root, object)
			defer store.Close()
			if mixed {
				if _, err := store.WriteSaveAt(object.ID, []byte{7}, 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Truncate(store.objects[object.ID].file.Name(), 777); err != nil {
				t.Fatal(err)
			}
			buffer := bytes.Repeat([]byte{0x55}, 4096)
			n, err := store.ReadSaveAt(object.ID, buffer, 0)
			if n != 777 || !errors.Is(err, io.EOF) {
				t.Fatalf("truncated base returned n=%d err=%v", n, err)
			}
			if !bytes.Equal(buffer[n:], bytes.Repeat([]byte{0x55}, len(buffer)-n)) {
				t.Fatal("short read modified bytes beyond returned count")
			}
		})
	}
}
