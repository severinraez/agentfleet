// Package capability reads the hub's rpc directory: what is in it, what each
// entry says about itself, and — the part that matters — which names from a
// sandbox are allowed to reach the filesystem at all.
package capability

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/severinraez/agentfleet/internal/protocol"
)

// Capability is one entry of the rpc directory.
type Capability struct {
	Name        string
	Description string
}

// ErrUnknown is returned for any name that does not resolve to a runnable
// capability, whatever the reason. The sandbox learns that the capability is
// unknown and nothing more: a name that is refused and a name that is absent
// should not be distinguishable from the outside.
var ErrUnknown = errors.New("unknown capability")

// ValidName reports whether name may be looked up in an rpc directory.
//
// The rule is protocol.ValidIdent: a single path element of ordinary
// characters, with no '/' and no leading dot, so "..", "a/b" and "/etc/passwd"
// cannot match. That is the whole point, and it is why Resolve validates
// before it joins.
func ValidName(name string) bool { return protocol.ValidIdent(name) }

// Resolve turns a name from a sandbox into a path to run, or ErrUnknown.
func Resolve(dir, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%w %q", ErrUnknown, name)
	}
	path := filepath.Join(dir, name)
	fi, err := os.Stat(path) // follows symlinks: a link to a script is fine
	if err != nil || !runnable(fi) {
		return "", fmt.Errorf("%w %q", ErrUnknown, name)
	}
	return path, nil
}

// List returns the capabilities of dir, sorted by name — os.ReadDir returns
// entries sorted by filename and filtering preserves that order.
func List(dir string) ([]Capability, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the rpc directory: %w", err)
	}
	caps := make([]Capability, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !ValidName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil || !runnable(fi) {
			// Not runnable, so not a capability. Listing it would promise
			// something the hub would then refuse.
			continue
		}
		caps = append(caps, Capability{Name: name, Description: Describe(path)})
	}
	return caps, nil
}

func runnable(fi os.FileInfo) bool {
	return fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

const (
	describeLines = 10
	describeBytes = 4 << 10
	describeMax   = 200
)

// Describe returns the "af:" self-description of a capability, or "".
//
// A wrapper introduces itself with a comment in its first few lines:
//
//	# af: send a message to the #agents channel
func Describe(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	head := make([]byte, describeBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ""
	}
	head = head[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		// A compiled binary. Whatever we found in there is not prose.
		return ""
	}

	scanner := bufio.NewScanner(bytes.NewReader(head))
	for i := 0; i < describeLines && scanner.Scan(); i++ {
		if d, ok := afComment(scanner.Text()); ok {
			return d
		}
	}
	return ""
}

func afComment(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, marker := range []string{"#!", "#", "//", ";", "--"} {
		if strings.HasPrefix(line, marker) {
			line = strings.TrimSpace(strings.TrimPrefix(line, marker))
			break
		}
	}
	rest, ok := strings.CutPrefix(line, "af:")
	if !ok {
		return "", false
	}
	return sanitize(strings.TrimSpace(rest)), true
}

// sanitize keeps a description to one printable line of bounded length: it
// ends up in a terminal listing, and the file it came from is not necessarily
// careful.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= describeMax {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
