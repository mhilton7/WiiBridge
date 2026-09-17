// Package scanner safely discovers WBFS and split-WBFS sets without modifying
// the source tree.
package scanner

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"wiibridge/shared/model"
	"wiibridge/shared/sourceidentity"
)

var gameID = regexp.MustCompile(`^[A-Z0-9]{6}$`)

type Rejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Result struct {
	Games      []model.Game `json:"games"`
	Rejected   []Rejection  `json:"rejected"`
	Root       string       `json:"root"`
	FileCount  int          `json:"file_count"`
	Platform   string       `json:"platform"`
	ScanStatus string       `json:"scan_status"`
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
	var candidates []string
	var rejected []Rejection
	err = filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("SOURCE-PARTIAL-SCAN: %w", walkErr)
		}
		if path == realRoot {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			rejected = append(rejected, Rejection{path, "symlink forbidden"})
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			if !d.IsDir() {
				rejected = append(rejected, Rejection{path, "special file forbidden"})
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".wbfs" {
			candidates = append(candidates, path)
		} else if strings.HasPrefix(ext, ".wbf") {
			// Segments are consumed with their .wbfs leader.
			if ext == ".wbf1" {
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	sort.Strings(candidates)
	seen := map[string]string{}
	var games []model.Game
	for _, path := range candidates {
		g, err := inspectSet(realRoot, path)
		if err != nil {
			rejected = append(rejected, Rejection{path, err.Error()})
			continue
		}
		key := strings.ToUpper(g.ID)
		if previous, ok := seen[key]; ok {
			rejected = append(rejected, Rejection{path, "duplicate game ID also at " + previous})
			continue
		}
		seen[key] = path
		games = append(games, g)
	}
	sort.Slice(games, func(i, j int) bool { return games[i].ID < games[j].ID })
	sort.Slice(rejected, func(i, j int) bool { return rejected[i].Path < rejected[j].Path })
	return Result{
		Games: games, Rejected: rejected, Root: realRoot, FileCount: len(candidates),
		Platform: "wii", ScanStatus: "complete",
	}, nil
}

func inspectSet(root, leader string) (model.Game, error) {
	clean := filepath.Clean(leader)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return model.Game{}, errors.New("path escapes library root")
	}
	f, err := os.Open(clean)
	if err != nil {
		return model.Game{}, err
	}
	defer f.Close()
	var header [512]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return model.Game{}, fmt.Errorf("truncated WBFS header: %w", err)
	}
	if !bytes.Equal(header[0:4], []byte("WBFS")) {
		return model.Game{}, errors.New("invalid WBFS magic")
	}
	hdSectors := binary.BigEndian.Uint32(header[4:8])
	hdShift, wbfsShift := header[8], header[9]
	if hdShift < 9 || hdShift > 20 || wbfsShift < hdShift || wbfsShift > 26 || hdSectors == 0 {
		return model.Game{}, errors.New("invalid WBFS geometry")
	}
	slot := -1
	for i, v := range header[12:] {
		if v != 0 {
			slot = i
			break
		}
	}
	if slot < 0 {
		return model.Game{}, errors.New("WBFS contains no disc")
	}
	discOffset := int64(slot+1) << wbfsShift
	var discHeader [0x60]byte
	if _, err = f.ReadAt(discHeader[:], discOffset); err != nil {
		return model.Game{}, errors.New("truncated WBFS disc metadata")
	}
	id := string(discHeader[0:6])
	if !gameID.MatchString(id) {
		return model.Game{}, errors.New("invalid disc game ID")
	}
	titleBytes := bytes.TrimRight(discHeader[0x20:0x60], "\x00 ")
	title := strings.ToValidUTF8(string(titleBytes), "?")
	if title == "" {
		title = id
	}
	var sources []model.Source
	var total int64
	base := strings.TrimSuffix(clean, filepath.Ext(clean))
	for segment := 0; ; segment++ {
		p := clean
		if segment > 0 {
			p = fmt.Sprintf("%s.wbf%d", base, segment)
		}
		info, statErr := os.Lstat(p)
		if statErr != nil {
			if segment == 0 {
				return model.Game{}, statErr
			}
			// A later segment existing after a gap is malformed.
			next := fmt.Sprintf("%s.wbf%d", base, segment+1)
			if _, nextErr := os.Lstat(next); nextErr == nil {
				return model.Game{}, fmt.Errorf("missing split segment wbf%d", segment)
			}
			break
		}
		if !info.Mode().IsRegular() {
			return model.Game{}, errors.New("split segment is not a regular file")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return model.Game{}, errors.New("source identity unavailable")
		}
		filesystemID, identityErr := sourceidentity.FilesystemID(p)
		if identityErr != nil {
			return model.Game{}, identityErr
		}
		sources = append(sources, model.Source{FilesystemID: filesystemID,
			Path: p, Offset: total, Length: info.Size(), Size: info.Size(),
			ModUnix: info.ModTime().UnixNano(), Device: uint64(st.Dev), Inode: st.Ino,
		})
		total += info.Size()
	}
	return model.Game{ID: id, Title: title, Sources: sources, Size: total}, nil
}

func VerifySource(s model.Source) error {
	info, err := os.Stat(s.Path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	filesystemID := ""
	if s.FilesystemID != "" {
		filesystemID, err = sourceidentity.FilesystemID(s.Path)
		if err != nil {
			return err
		}
	}
	if !ok || info.Size() != s.Size || info.ModTime().UnixNano() != s.ModUnix ||
		!sourceidentity.SameFilesystem(s.FilesystemID, s.Device, filesystemID, uint64(st.Dev)) || st.Ino != s.Inode {
		return errors.New("source identity changed")
	}
	return nil
}
