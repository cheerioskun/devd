// exp13-disk-bench measures storage operations from inside a Linux guest.
// It intentionally uses only the Go standard library so it can be cross-built
// as a static linux/arm64 binary from macOS.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type results struct {
	Mode               string  `json:"mode"`
	Location           string  `json:"location"`
	Round              int     `json:"round"`
	SizeMiB            int     `json:"size_mib"`
	Files              int     `json:"files"`
	RandomOps          int     `json:"random_ops"`
	FsyncOps           int     `json:"fsync_ops"`
	DropCaches         bool    `json:"drop_caches"`
	SeqWriteMiBs       float64 `json:"seq_write_mib_s"`
	SeqReadMiBs        float64 `json:"seq_read_mib_s"`
	RandomReadIOPS     float64 `json:"random_read_iops"`
	RandomWriteIOPS    float64 `json:"random_write_iops"`
	FsyncP50Millis     float64 `json:"fsync_p50_ms"`
	FsyncP95Millis     float64 `json:"fsync_p95_ms"`
	CreateOpsPerSecond float64 `json:"create_ops_s"`
	StatOpsPerSecond   float64 `json:"stat_ops_s"`
	RenameOpsPerSecond float64 `json:"rename_ops_s"`
	DeleteOpsPerSecond float64 `json:"delete_ops_s"`
	Checksum           uint64  `json:"checksum"`
}

func main() {
	var (
		dir       = flag.String("dir", "", "benchmark directory")
		mode      = flag.String("mode", "unknown", "storage mode label")
		location  = flag.String("location", "root", "root or workspace")
		round     = flag.Int("round", 1, "benchmark round")
		sizeMiB   = flag.Int("size-mib", 256, "sequential test file size in MiB")
		files     = flag.Int("files", 5000, "metadata file count")
		randomOps = flag.Int("random-ops", 25000, "4 KiB random operations")
		fsyncOps  = flag.Int("fsync-ops", 200, "4 KiB write+fsync operations")
		seed      = flag.Uint64("seed", 1, "random offset seed")
	)
	flag.Parse()

	if *dir == "" || *sizeMiB <= 0 || *files <= 0 || *randomOps <= 0 || *fsyncOps <= 0 {
		fmt.Fprintln(os.Stderr, "dir and positive workload sizes are required")
		os.Exit(2)
	}

	if err := os.RemoveAll(*dir); err != nil {
		fatal("remove old benchmark directory", err)
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal("create benchmark directory", err)
	}
	defer func() { _ = os.RemoveAll(*dir) }()

	result := results{
		Mode:      *mode,
		Location:  *location,
		Round:     *round,
		SizeMiB:   *sizeMiB,
		Files:     *files,
		RandomOps: *randomOps,
		FsyncOps:  *fsyncOps,
	}

	dataPath := filepath.Join(*dir, "data.bin")
	block := incompressibleBlock(1 << 20)
	dataBytes := int64(*sizeMiB) << 20

	started := time.Now()
	f, err := os.OpenFile(dataPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		fatal("create sequential file", err)
	}
	for remaining := dataBytes; remaining > 0; {
		chunk := block
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		if _, err := f.Write(chunk); err != nil {
			fatal("sequential write", err)
		}
		remaining -= int64(len(chunk))
	}
	if err := f.Sync(); err != nil {
		fatal("sync sequential file", err)
	}
	if err := f.Close(); err != nil {
		fatal("close sequential file", err)
	}
	result.SeqWriteMiBs = float64(*sizeMiB) / time.Since(started).Seconds()

	result.DropCaches = dropCaches()
	f, err = os.Open(dataPath)
	if err != nil {
		fatal("open sequential file", err)
	}
	readBlock := make([]byte, 1<<20)
	started = time.Now()
	var checksum uint64
	for {
		n, readErr := f.Read(readBlock)
		if n > 0 {
			checksum += uint64(readBlock[0]) + uint64(readBlock[n-1])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			fatal("sequential read", readErr)
		}
	}
	result.SeqReadMiBs = float64(*sizeMiB) / time.Since(started).Seconds()
	if err := f.Close(); err != nil {
		fatal("close sequential read file", err)
	}

	result.DropCaches = dropCaches() && result.DropCaches
	f, err = os.Open(dataPath)
	if err != nil {
		fatal("open random-read file", err)
	}
	randomBlock := make([]byte, 4096)
	blocks := dataBytes / int64(len(randomBlock))
	rng := newXorshift(*seed + uint64(*round)*0x9e3779b97f4a7c15)
	started = time.Now()
	for i := 0; i < *randomOps; i++ {
		offset := int64(rng.next()%uint64(blocks)) * int64(len(randomBlock))
		if _, err := f.ReadAt(randomBlock, offset); err != nil {
			fatal("random read", err)
		}
		checksum += uint64(randomBlock[0])
	}
	result.RandomReadIOPS = float64(*randomOps) / time.Since(started).Seconds()
	if err := f.Close(); err != nil {
		fatal("close random-read file", err)
	}

	f, err = os.OpenFile(dataPath, os.O_RDWR, 0)
	if err != nil {
		fatal("open random-write file", err)
	}
	rng = newXorshift(*seed + uint64(*round)*0xd1b54a32d192ed03)
	started = time.Now()
	for i := 0; i < *randomOps; i++ {
		offset := int64(rng.next()%uint64(blocks)) * int64(len(randomBlock))
		randomBlock[0] = byte(i)
		if _, err := f.WriteAt(randomBlock, offset); err != nil {
			fatal("random write", err)
		}
	}
	if err := f.Sync(); err != nil {
		fatal("sync random writes", err)
	}
	result.RandomWriteIOPS = float64(*randomOps) / time.Since(started).Seconds()
	if err := f.Close(); err != nil {
		fatal("close random-write file", err)
	}

	fsyncPath := filepath.Join(*dir, "fsync.bin")
	f, err = os.OpenFile(fsyncPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		fatal("create fsync file", err)
	}
	latencies := make([]time.Duration, 0, *fsyncOps)
	for i := 0; i < *fsyncOps; i++ {
		randomBlock[0] = byte(i)
		started = time.Now()
		if _, err := f.WriteAt(randomBlock, 0); err != nil {
			fatal("fsync write", err)
		}
		if err := f.Sync(); err != nil {
			fatal("fsync", err)
		}
		latencies = append(latencies, time.Since(started))
	}
	if err := f.Close(); err != nil {
		fatal("close fsync file", err)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result.FsyncP50Millis = durationMillis(percentile(latencies, 0.50))
	result.FsyncP95Millis = durationMillis(percentile(latencies, 0.95))

	metaDir := filepath.Join(*dir, "metadata")
	if err := os.Mkdir(metaDir, 0o755); err != nil {
		fatal("create metadata directory", err)
	}
	payload := []byte("exp13 metadata payload\n")
	started = time.Now()
	for i := 0; i < *files; i++ {
		path := filepath.Join(metaDir, fmt.Sprintf("file-%08d", i))
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			fatal("create metadata file", err)
		}
	}
	result.CreateOpsPerSecond = float64(*files) / time.Since(started).Seconds()

	started = time.Now()
	for i := 0; i < *files; i++ {
		path := filepath.Join(metaDir, fmt.Sprintf("file-%08d", i))
		info, err := os.Stat(path)
		if err != nil {
			fatal("stat metadata file", err)
		}
		checksum += uint64(info.Size())
	}
	result.StatOpsPerSecond = float64(*files) / time.Since(started).Seconds()

	started = time.Now()
	for i := 0; i < *files; i++ {
		oldPath := filepath.Join(metaDir, fmt.Sprintf("file-%08d", i))
		newPath := filepath.Join(metaDir, fmt.Sprintf("renamed-%08d", i))
		if err := os.Rename(oldPath, newPath); err != nil {
			fatal("rename metadata file", err)
		}
	}
	result.RenameOpsPerSecond = float64(*files) / time.Since(started).Seconds()

	started = time.Now()
	for i := 0; i < *files; i++ {
		path := filepath.Join(metaDir, fmt.Sprintf("renamed-%08d", i))
		if err := os.Remove(path); err != nil {
			fatal("delete metadata file", err)
		}
	}
	result.DeleteOpsPerSecond = float64(*files) / time.Since(started).Seconds()
	result.Checksum = checksum

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fatal("encode results", err)
	}
}

func dropCaches() bool {
	_ = syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200) == nil
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	index := int(float64(len(values)-1) * fraction)
	return values[index]
}

func durationMillis(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func incompressibleBlock(size int) []byte {
	data := make([]byte, size)
	rng := newXorshift(0x243f6a8885a308d3)
	for i := range data {
		data[i] = byte(rng.next())
	}
	return data
}

type xorshift struct {
	state uint64
}

func newXorshift(seed uint64) *xorshift {
	if seed == 0 {
		seed = 1
	}
	return &xorshift{state: seed}
}

func (x *xorshift) next() uint64 {
	x.state ^= x.state << 13
	x.state ^= x.state >> 7
	x.state ^= x.state << 17
	return x.state
}

func fatal(operation string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	os.Exit(1)
}
