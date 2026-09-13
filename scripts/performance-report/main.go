package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"wiibridge/server/host-daemon/scanner"
	"wiibridge/server/host-daemon/vdisk"
	"wiibridge/tests/testutil"
)

type result struct {
	RequestBytes int     `json:"request_bytes"`
	Pattern      string  `json:"pattern"`
	MedianUS     float64 `json:"median_us"`
	P95US        float64 `json:"p95_us"`
	P99US        float64 `json:"p99_us"`
	MaxUS        float64 `json:"maximum_us"`
	ThroughputMB float64 `json:"throughput_mib_s"`
}

func main() {
	root, err := os.MkdirTemp("", "wiibridge-performance-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	if err = testutil.SyntheticWBFS(filepath.Join(root, "PERF01.wbfs"), "PERF01", "Performance", 64<<20); err != nil {
		panic(err)
	}
	scan, err := scanner.Scan(root)
	if err != nil {
		panic(err)
	}
	disk, err := vdisk.Build("performance", scan.Games, "test")
	if err != nil {
		panic(err)
	}
	var results []result
	for _, size := range []int{4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20} {
		for _, pattern := range []string{"sequential", "random", "metadata-heavy"} {
			results = append(results, measure(disk, size, pattern))
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"results": results, "heap_alloc_bytes": mem.HeapAlloc,
		"iterations_per_case": 200,
		"cache":               "uncontrolled OS cache; source opened per payload read",
		"fixture":             "sparse synthetic WBFS; virtual reads mix metadata, payload and zero-fill",
	})
}

func measure(disk *vdisk.Disk, size int, pattern string) result {
	samples := make([]float64, 200)
	buf := make([]byte, size)
	var total time.Duration
	for i := range samples {
		var off int64
		switch pattern {
		case "sequential":
			off = int64(i*size) % (disk.Size() - int64(size))
		case "random":
			off = rand.New(rand.NewSource(int64(i + size))).Int63n(disk.Size() - int64(size))
		default:
			off = int64((i%256)*512) % (disk.Size() - int64(size))
		}
		start := time.Now()
		if _, err := disk.ReadAt(buf, off); err != nil {
			panic(err)
		}
		elapsed := time.Since(start)
		total += elapsed
		samples[i] = float64(elapsed.Nanoseconds()) / 1000
	}
	sort.Float64s(samples)
	seconds := total.Seconds()
	return result{RequestBytes: size, Pattern: pattern, MedianUS: samples[100],
		P95US: samples[190], P99US: samples[198], MaxUS: samples[199],
		ThroughputMB: float64(size*len(samples)) / (1 << 20) / seconds}
}
