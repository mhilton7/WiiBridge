package vdisk

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"wiibridge/shared/model"
)

func TestIndexedReadsPreserveSourcesPaddingGapsAndBoundaries(t *testing.T) {
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	path := filepath.Join(t.TempDir(), "synthetic.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	source := model.Source{Path: path, Length: int64(len(data)), Size: int64(len(data)),
		ModUnix: info.ModTime().UnixNano(), Device: uint64(stat.Dev), Inode: stat.Ino}
	const count = 10000
	disk := &Disk{size: (count + 4) * sectorSize,
		metadata: map[int64][]byte{0: bytes.Repeat([]byte{0xab}, int(sectorSize))}}
	want := make([]byte, disk.size)
	copy(want, disk.metadata[0])
	for i := 0; i < count; i++ {
		start := int64(i+2) * sectorSize
		sourceOffset := int64((i * 37) % (len(data) - 300))
		disk.extents = append(disk.extents,
			extent{start: start, length: 300, source: &source, sourceOffset: sourceOffset},
			extent{start: start + 300, length: 100, zero: true})
		copy(want[start:start+300], data[sourceOffset:sourceOffset+300])
		// The remaining 112 bytes are an unmapped gap, distinct from padding.
	}
	check := func(offset int64, length int) {
		t.Helper()
		got := bytes.Repeat([]byte{0xff}, length)
		if n, err := disk.ReadAt(got, offset); err != nil || n != length {
			t.Fatalf("read at %d: n=%d err=%v", offset, n, err)
		}
		if !bytes.Equal(got, want[offset:offset+int64(length)]) {
			t.Fatalf("mapped bytes differ at %d, length %d", offset, length)
		}
	}
	check(0, 2048)
	check(1024+299, 400)
	check(disk.size-700, 700)
	check(disk.size, 0)
	random := rand.New(rand.NewSource(17))
	for i := 0; i < 100; i++ {
		length := 1 + random.Intn(4096)
		check(random.Int63n(disk.size-int64(length)), length)
	}
	for _, offset := range []int64{-1, disk.size, disk.size + 1} {
		if _, err := disk.ReadAt(make([]byte, 1), offset); err != io.EOF {
			t.Fatalf("out-of-bounds read at %d: %v", offset, err)
		}
	}
	buffer := make([]byte, 100)
	allocations := func(offset int64) float64 {
		return testing.AllocsPerRun(100, func() {
			if _, err := disk.ReadAt(buffer, offset); err != nil {
				panic(err)
			}
		})
	}
	first, last := allocations(1024), allocations(int64(count+1)*sectorSize)
	if last > first+1 {
		t.Fatalf("read allocations scale with catalog position: first=%g last=%g", first, last)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatal("read modified source bytes")
	}
}

func TestBatchedFATReadsMatchEntryReferenceAcrossBothCopies(t *testing.T) {
	disk := &Disk{fatStart: 8, fatSectors: 512, size: 1040 * sectorSize}
	cluster := uint32(2)
	for i := 0; i < 4096; i++ {
		count := uint32(i%9 + 1)
		disk.fatChains = append(disk.fatChains, clusterChain{first: cluster, count: count})
		cluster += count + uint32(i%5)
	}
	// The entry-at-a-time implementation provides a separate reference for
	// reserved clusters, chain ends, holes, and unused FAT tail entries.
	fat := make([]byte, disk.fatSectors*sectorSize)
	for cluster := uint32(0); cluster < uint32(len(fat)/4); cluster++ {
		binary.LittleEndian.PutUint32(fat[int(cluster)*4:], disk.fatValue(cluster))
	}
	want := make([]byte, disk.size)
	for copyIndex := int64(0); copyIndex < 2; copyIndex++ {
		copy(want[(disk.fatStart+copyIndex*disk.fatSectors)*sectorSize:], fat)
	}
	for _, request := range []struct{ offset, length int64 }{
		{0, disk.size},
		{disk.fatStart*sectorSize - 117, 65536},
		{(disk.fatStart+disk.fatSectors)*sectorSize - 19, 65536 + 37},
		{(disk.fatStart+2*disk.fatSectors)*sectorSize - 111, 300},
	} {
		got := bytes.Repeat([]byte{0xff}, int(request.length))
		if n, err := disk.ReadAt(got, request.offset); err != nil || int64(n) != request.length {
			t.Fatalf("FAT read: n=%d err=%v", n, err)
		}
		if !bytes.Equal(got, want[request.offset:request.offset+request.length]) {
			t.Fatalf("FAT bytes differ at %d, length %d", request.offset, request.length)
		}
	}
	buffer := make([]byte, 65536)
	if allocations := testing.AllocsPerRun(100, func() {
		if _, err := disk.ReadAt(buffer, disk.fatStart*sectorSize+117); err != nil {
			panic(err)
		}
	}); allocations != 0 {
		t.Fatalf("FAT read allocated %g objects", allocations)
	}
}
