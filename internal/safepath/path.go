package safepath

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Relative accepts portable regular-file paths. It rejects ambiguous Windows
// spellings on all hosts so approvals cannot change meaning across platforms.
func Relative(name string) error {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, "\\:\x00") {
		return errors.New("invalid relative path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || part == "" || strings.TrimRight(part, " .") != part {
			return errors.New("ambiguous path component")
		}
		for _, c := range part {
			if c < 32 || c == 127 || strings.ContainsRune(`<>"|?*`, c) {
				return errors.New("unsupported path character")
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || len([]rune(base)) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("0123456789¹²³", []rune(base)[3]) {
			return errors.New("reserved device path")
		}
	}
	return nil
}

// Writable rejects agent/control configuration in addition to path aliases.
// This floor cannot be relaxed by a model-provided file proposal.
func Writable(name string) error {
	if err := Relative(name); err != nil {
		return err
	}
	parts := strings.Split(strings.ToLower(name), "/")
	for _, p := range parts {
		switch p {
		case ".git", ".harness", ".codex", ".agents", "agents.md", "harness.toml":
			return errors.New("protected control path")
		}
		if strings.HasPrefix(p, ".harness-tmp-") {
			return errors.New("reserved temporary path")
		}
	}
	if len(parts) > 1 && parts[0] == ".github" && parts[1] == "workflows" {
		return errors.New("protected workflow configuration")
	}
	return nil
}

// Directory rejects link/reparse ancestors before opening an absolute root.
// Actual file operations must still use os.Root to contain concurrent traversal.
func Directory(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("absolute root required")
	}
	for p := filepath.Clean(root); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || linked(info) {
			return errors.New("linked or non-directory root")
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return nil
}

// Check rejects symlink/reparse components within an already opened root. Only
// a missing suffix is allowed when allowMissing is true; existing ancestors
// must still pass validation before a caller may create new directories.
func Check(root *os.Root, name string, allowMissing bool) error {
	if err := Relative(name); err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(p)
		if err != nil {
			if allowMissing && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if linked(info) {
			return errors.New("linked path component")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("non-directory ancestor")
		}
	}
	return nil
}

// ReadRegular hashes bounded bytes from a regular, singly linked file. A missing
// final entry returns exists=false; it is never confused with empty file content.
func ReadRegular(root *os.Root, name string, limit int64) (digest string, size int64, executable bool, exists bool, err error) {
	return CopyRegular(root, name, limit, io.Discard)
}

// CopyRegular streams the same regular-file observation that supplies its hash.
// Destination bytes are untrusted until success; errors can leave partial output.
func CopyRegular(root *os.Root, name string, limit int64, destination io.Writer) (digest string, size int64, executable bool, exists bool, err error) {
	if destination == nil || limit < 0 || limit > 1<<30 {
		return "", 0, false, false, errors.New("invalid regular-file copy bounds or destination")
	}
	if err = Check(root, name, true); err != nil {
		return
	}
	f, e := root.Open(name)
	if os.IsNotExist(e) {
		return "", 0, false, false, nil
	}
	if e != nil {
		err = e
		return
	}
	defer f.Close()
	exists = true
	info, e := f.Stat()
	if e != nil {
		err = e
		return
	}
	if !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
		return
	}
	if e = singleLink(f); e != nil {
		err = e
		return
	}
	if info.Size() > limit {
		err = errors.New("file size bound exceeded")
		return
	}
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(h, destination), io.LimitReader(f, limit+1))
	if err != nil {
		return
	}
	if size > limit {
		err = errors.New("file grew beyond bound")
		return
	}
	after, e := f.Stat()
	if e != nil {
		err = e
		return
	}
	if size != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		err = errors.New("file changed while observing")
		return
	}
	if err = singleLink(f); err != nil {
		return
	}
	digest = hex.EncodeToString(h.Sum(nil))
	executable = info.Mode().Perm()&0111 != 0
	return
}

// RequireDigest validates a SHA-256 identity for a file or canonical artifact.
func RequireDigest(s string) error {
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid SHA-256 identity")
	}
	return nil
}

// EnsureDirectory creates relative state directories one component at a time,
// rejecting existing aliases before following them. It never removes anything.
func EnsureDirectory(root, name string) error {
	if err := Directory(root); err != nil {
		return err
	}
	if err := Relative(name); err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := r.Lstat(p)
		if os.IsNotExist(err) {
			if err = r.Mkdir(p, 0700); err != nil {
				return err
			}
			info, err = r.Lstat(p)
		}
		if err != nil {
			return err
		}
		if linked(info) || !info.IsDir() {
			return errors.New("state directory alias rejected")
		}
	}
	return nil
}
