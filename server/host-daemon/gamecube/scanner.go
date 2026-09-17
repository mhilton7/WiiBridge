// Package gamecube discovers and validates Nintendont-compatible GameCube
// sources without modifying them.
package gamecube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	gameCubeMagic = uint32(0xc2339f3d)
	headerSize    = 0x440
	cisoHeader    = int64(0x8000)
	cisoBlock     = int64(2 << 20)
	cisoMapSize   = 1024
	maxDiscSize   = int64(2 << 30)
)

var validID = regexp.MustCompile(`^[A-Z0-9]{6}$`)

type Disc struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Region       string `json:"region"`
	Revision     byte   `json:"revision"`
	Number       byte   `json:"disc_number"`
	SourcePath   string `json:"source_path"`
	Format       string `json:"format"`
	LogicalSize  int64  `json:"logical_size"`
	PhysicalSize int64  `json:"physical_size"`
	SHA256       string `json:"sha256"`
	Validation   string `json:"validation_status"`
}

type Game struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Region       string `json:"region"`
	Revision     byte   `json:"revision"`
	DiscCount    int    `json:"disc_count"`
	Format       string `json:"format"`
	Validation   string `json:"validation_status"`
	Discs        []Disc `json:"discs"`
	Availability string `json:"availability,omitempty"`
}

type Rejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Result struct {
	Games      []Game      `json:"games"`
	Rejected   []Rejection `json:"rejected"`
	Root       string      `json:"root"`
	FileCount  int         `json:"file_count"`
	Platform   string      `json:"platform"`
	ScanStatus string      `json:"scan_status"`
}

func Scan(root string) (Result, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Result{}, err
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return Result{}, err
	}
	var discs []Disc
	var rejected []Rejection
	var count int
	err = filepath.WalkDir(realRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("SOURCE-PARTIAL-SCAN: %w", walkErr)
		}
		if path == realRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			rejected = append(rejected, Rejection{Path: path, Reason: "symlink forbidden"})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if isFSTCandidate(path) {
				count++
				disc, inspectErr := inspectFST(realRoot, path)
				if inspectErr != nil {
					rejected = append(rejected, Rejection{Path: path, Reason: inspectErr.Error()})
				} else {
					discs = append(discs, disc)
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			rejected = append(rejected, Rejection{Path: path, Reason: "special file forbidden"})
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".iso", ".gcm", ".ciso", ".cso":
			count++
			if strings.Contains(strings.ToLower(filepath.Base(path)), ".nkit.") {
				rejected = append(rejected, Rejection{Path: path, Reason: "NKit images are unsupported"})
				return nil
			}
			disc, inspectErr := inspectImage(realRoot, path, ext)
			if inspectErr != nil {
				rejected = append(rejected, Rejection{Path: path, Reason: inspectErr.Error()})
			} else {
				discs = append(discs, disc)
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	games, pairRejects := pair(discs)
	rejected = append(rejected, pairRejects...)
	sort.Slice(rejected, func(i, j int) bool { return rejected[i].Path < rejected[j].Path })
	return Result{
		Games: games, Rejected: rejected, Root: realRoot, FileCount: count,
		Platform: "gamecube", ScanStatus: "complete",
	}, nil
}

func isFSTCandidate(path string) bool {
	info, err := os.Lstat(filepath.Join(path, "sys", "boot.bin"))
	return err == nil && info.Mode().IsRegular()
}

func inspectImage(root, path, ext string) (Disc, error) {
	if err := within(root, path); err != nil {
		return Disc{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Disc{}, err
	}
	if !info.Mode().IsRegular() {
		return Disc{}, errors.New("source is not a regular file")
	}
	if info.Size() < headerSize || info.Size() > maxDiscSize {
		return Disc{}, errors.New("impossible or truncated disc size")
	}
	f, err := os.Open(path)
	if err != nil {
		return Disc{}, err
	}
	defer f.Close()
	var reader io.ReaderAt = f
	logicalSize := info.Size()
	format := strings.TrimPrefix(ext, ".")
	if ext == ".ciso" || ext == ".cso" {
		ciso, cisoErr := newCISOReader(f, info.Size())
		if cisoErr != nil {
			return Disc{}, cisoErr
		}
		reader = ciso
		logicalSize = ciso.logicalSize()
		format = "ciso"
	}
	header, err := readAndValidateHeader(reader, logicalSize)
	if err != nil {
		return Disc{}, err
	}
	if err = validateFST(reader, header, logicalSize); err != nil {
		return Disc{}, err
	}
	// Full-image hashing is intentionally deferred until Prepare export.
	// Catalog startup must not synchronously read every byte of every disc.
	return discFromHeader(header, path, format, logicalSize, info.Size(), ""), nil
}

func inspectFST(root, path string) (Disc, error) {
	if err := within(root, path); err != nil {
		return Disc{}, err
	}
	required := []string{
		"sys/boot.bin", "sys/bi2.bin", "sys/apploader.img", "sys/main.dol",
	}
	for _, relative := range required {
		full := filepath.Join(path, filepath.FromSlash(relative))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return Disc{}, fmt.Errorf("missing or empty required FST file %s", relative)
		}
	}
	filesInfo, err := os.Lstat(filepath.Join(path, "files"))
	if err != nil || !filesInfo.IsDir() {
		return Disc{}, errors.New("missing FST files directory")
	}
	boot, err := os.ReadFile(filepath.Join(path, "sys", "boot.bin"))
	if err != nil {
		return Disc{}, err
	}
	if len(boot) < 0x100 {
		return Disc{}, errors.New("truncated FST boot.bin")
	}
	if _, err = validateHeader(boot); err != nil {
		return Disc{}, err
	}
	physicalSize, err := measureTree(path)
	if err != nil {
		return Disc{}, err
	}
	// Full-tree hashing is deliberately deferred to an explicit generation
	// build or deep validation. Routine catalog startup only verifies the
	// structure and records its apparent payload size.
	return discFromHeader(boot, path, "fst", physicalSize, physicalSize, ""), nil
}

func measureTree(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink forbidden in extracted FST")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("special file forbidden in extracted FST")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 0 && total > maxDiscSize-info.Size() {
			return errors.New("extracted FST exceeds supported disc size")
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	if total <= 0 || total > maxDiscSize {
		return 0, errors.New("invalid extracted FST size")
	}
	return total, nil
}

func readAndValidateHeader(reader io.ReaderAt, logicalSize int64) ([]byte, error) {
	header := make([]byte, headerSize)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return nil, fmt.Errorf("truncated GameCube header: %w", err)
	}
	if _, err := validateHeader(header); err != nil {
		return nil, err
	}
	if logicalSize < headerSize || logicalSize > maxDiscSize {
		return nil, errors.New("invalid logical disc size")
	}
	return header, nil
}

func validateHeader(header []byte) (string, error) {
	if len(header) < 0x100 {
		return "", errors.New("truncated GameCube header")
	}
	id := string(header[:6])
	if !validID.MatchString(id) {
		return "", errors.New("invalid GameCube game ID")
	}
	if binary.BigEndian.Uint32(header[0x1c:0x20]) != gameCubeMagic {
		return "", errors.New("invalid GameCube magic")
	}
	if header[6] > 1 {
		return "", errors.New("unsupported disc number")
	}
	return id, nil
}

func validateFST(reader io.ReaderAt, header []byte, logicalSize int64) error {
	offset := int64(binary.BigEndian.Uint32(header[0x424:0x428]))
	size := int64(binary.BigEndian.Uint32(header[0x428:0x42c]))
	if offset < headerSize || size < 12 || offset > logicalSize || size > logicalSize-offset {
		return errors.New("invalid or unreadable filesystem table")
	}
	root := make([]byte, 12)
	if _, err := reader.ReadAt(root, offset); err != nil {
		return fmt.Errorf("unreadable filesystem table: %w", err)
	}
	if root[0] != 1 {
		return errors.New("filesystem table root is not a directory")
	}
	entries := binary.BigEndian.Uint32(root[8:12])
	if entries == 0 || int64(entries)*12 > size {
		return errors.New("invalid filesystem table entry count")
	}
	return nil
}

func discFromHeader(header []byte, path, format string, logical, physical int64, sum string) Disc {
	title := strings.ToValidUTF8(string(bytes.TrimRight(header[0x20:0x60], "\x00 ")), "?")
	if title == "" {
		title = string(header[:6])
	}
	id := string(header[:6])
	return Disc{
		ID: id, Title: title, Region: region(id[3]), Revision: header[7],
		Number: header[6], SourcePath: path, Format: format,
		LogicalSize: logical, PhysicalSize: physical, SHA256: sum, Validation: "valid",
	}
}

func region(code byte) string {
	switch code {
	case 'E', 'N':
		return "NTSC-U"
	case 'J':
		return "NTSC-J"
	case 'K', 'Q', 'T':
		return "NTSC-K"
	case 'P', 'D', 'F', 'H', 'I', 'L', 'M', 'R', 'S', 'U', 'V', 'X', 'Y':
		return "PAL"
	default:
		return "Unknown"
	}
}

func pair(discs []Disc) ([]Game, []Rejection) {
	sort.Slice(discs, func(i, j int) bool {
		if discs[i].ID != discs[j].ID {
			return discs[i].ID < discs[j].ID
		}
		if discs[i].Revision != discs[j].Revision {
			return discs[i].Revision < discs[j].Revision
		}
		if discs[i].Number != discs[j].Number {
			return discs[i].Number < discs[j].Number
		}
		return discs[i].SourcePath < discs[j].SourcePath
	})
	type key struct {
		id       string
		revision byte
	}
	grouped := make(map[key][]Disc)
	var keys []key
	for _, disc := range discs {
		k := key{disc.ID, disc.Revision}
		if _, ok := grouped[k]; !ok {
			keys = append(keys, k)
		}
		grouped[k] = append(grouped[k], disc)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].id == keys[j].id {
			return keys[i].revision < keys[j].revision
		}
		return keys[i].id < keys[j].id
	})
	var games []Game
	var rejected []Rejection
	for _, k := range keys {
		set := grouped[k]
		byNumber := make(map[byte]Disc)
		valid := true
		for _, disc := range set {
			if previous, exists := byNumber[disc.Number]; exists {
				rejected = append(rejected,
					Rejection{Path: disc.SourcePath, Reason: "duplicate disc number also at " + previous.SourcePath})
				valid = false
				continue
			}
			byNumber[disc.Number] = disc
		}
		first, hasFirst := byNumber[0]
		if !hasFirst {
			for _, disc := range set {
				rejected = append(rejected, Rejection{Path: disc.SourcePath, Reason: "disc two has no matching disc one"})
			}
			continue
		}
		if !valid {
			continue
		}
		ordered := []Disc{first}
		if second, ok := byNumber[1]; ok {
			if second.ID != first.ID || second.Region != first.Region || second.Revision != first.Revision {
				rejected = append(rejected, Rejection{Path: second.SourcePath, Reason: "disc two metadata mismatch"})
				continue
			}
			ordered = append(ordered, second)
		}
		format := first.Format
		if len(ordered) == 2 && ordered[1].Format != format {
			format = "mixed"
		}
		games = append(games, Game{
			ID: first.ID, Title: first.Title, Region: first.Region, Revision: first.Revision,
			DiscCount: len(ordered), Format: format, Validation: "valid", Discs: ordered,
		})
	}
	return games, rejected
}

func within(root, path string) error {
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path escapes library root")
	}
	return nil
}

func hashFile(path string) (string, error) {
	return hashFileContext(context.Background(), path)
}

func hashFileContext(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx: ctx, reader: f}); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Wrapping the file also prevents io.Copy from bypassing cancellation through
// WriterTo. Cancellation is checked between bounded reads; a blocked storage
// read still has to return before cancellation can take effect.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) > 32<<10 {
		buffer = buffer[:32<<10]
	}
	return reader.reader.Read(buffer)
}

func hashTree(root string) (string, int64, error) {
	return hashTreeContext(context.Background(), root)
}

func hashTreeContext(ctx context.Context, root string) (string, int64, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink forbidden in extracted FST")
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("special file forbidden in extracted FST")
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	var total int64
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		relative, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return "", 0, err
		}
		total += info.Size()
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(relative), info.Size())
		f, err := os.Open(path)
		if err != nil {
			return "", 0, err
		}
		_, copyErr := io.Copy(hash, contextReader{ctx: ctx, reader: f})
		closeErr := f.Close()
		if copyErr != nil {
			return "", 0, copyErr
		}
		if closeErr != nil {
			return "", 0, closeErr
		}
	}
	if err = ctx.Err(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), total, nil
}

type cisoReader struct {
	file    *os.File
	mapData [cisoMapSize]byte
	size    int64
}

func newCISOReader(file *os.File, physicalSize int64) (*cisoReader, error) {
	header := make([]byte, cisoHeader)
	if _, err := file.ReadAt(header, 0); err != nil {
		return nil, errors.New("truncated CISO header")
	}
	if !bytes.Equal(header[:4], []byte("CISO")) ||
		binary.LittleEndian.Uint32(header[4:8]) != uint32(cisoBlock) {
		return nil, errors.New("unsupported CISO geometry; Nintendont requires 2 MiB blocks")
	}
	reader := &cisoReader{file: file, size: physicalSize}
	copy(reader.mapData[:], header[8:8+cisoMapSize])
	var present int64
	for _, mapped := range reader.mapData {
		if mapped != 0 {
			present++
		}
	}
	if present == 0 || cisoHeader+present*cisoBlock > physicalSize {
		return nil, errors.New("truncated or invalid CISO block map")
	}
	return reader, nil
}

func (c *cisoReader) logicalSize() int64 {
	for index := len(c.mapData) - 1; index >= 0; index-- {
		if c.mapData[index] != 0 {
			return int64(index+1) * cisoBlock
		}
	}
	return 0
}

func (c *cisoReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= c.logicalSize() {
		return 0, io.EOF
	}
	done := 0
	for done < len(p) && off+int64(done) < c.logicalSize() {
		position := off + int64(done)
		block := position / cisoBlock
		inBlock := position % cisoBlock
		length := len(p) - done
		if available := cisoBlock - inBlock; int64(length) > available {
			length = int(available)
		}
		if c.mapData[block] == 0 {
			clear(p[done : done+length])
		} else {
			var physicalIndex int64
			for index := int64(0); index < block; index++ {
				if c.mapData[index] != 0 {
					physicalIndex++
				}
			}
			n, err := c.file.ReadAt(p[done:done+length], cisoHeader+physicalIndex*cisoBlock+inBlock)
			done += n
			if err != nil {
				return done, err
			}
			continue
		}
		done += length
	}
	if done != len(p) {
		return done, io.EOF
	}
	return done, nil
}
