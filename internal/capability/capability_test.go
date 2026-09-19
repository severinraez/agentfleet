package capability

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds an rpc directory with one of everything the hub can meet.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	writeFile(t, dir, "deploy", 0o755, "#!/bin/sh\n# af: deploy the current branch to staging\nexec /opt/deploy.sh\n")
	writeFile(t, dir, "notify", 0o755, "#!/usr/bin/env python3\nimport sys\n# af: send a message to the #agents channel\n")
	writeFile(t, dir, "plain", 0o755, "#!/bin/sh\necho no description here\n")
	writeFile(t, dir, "readme.txt", 0o644, "# af: not executable, so not a capability\n")
	writeFile(t, dir, ".hidden", 0o755, "#!/bin/sh\n# af: hidden\n")
	writeFile(t, dir, "compiled", 0o755, "\x7fELF\x00\x00# af: not prose\n")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "deploy"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, dir, name string, mode os.FileMode, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	caps, err := List(fixture(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Sorted, and only what can actually be run.
	want := []Capability{
		{Name: "compiled", Description: ""},
		{Name: "deploy", Description: "deploy the current branch to staging"},
		{Name: "link", Description: "deploy the current branch to staging"},
		{Name: "notify", Description: "send a message to the #agents channel"},
		{Name: "plain", Description: ""},
	}
	if len(caps) != len(want) {
		var names []string
		for _, c := range caps {
			names = append(names, c.Name)
		}
		t.Fatalf("List returned %v, want %d entries", names, len(want))
	}
	for i, w := range want {
		if caps[i].Name != w.Name || caps[i].Description != w.Description {
			t.Errorf("entry %d = %q/%q, want %q/%q", i, caps[i].Name, caps[i].Description, w.Name, w.Description)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	dir := fixture(t)

	if path, err := Resolve(dir, "deploy"); err != nil || path != filepath.Join(dir, "deploy") {
		t.Errorf("Resolve(deploy) = %q, %v", path, err)
	}
	if _, err := Resolve(dir, "link"); err != nil {
		t.Errorf("Resolve(link): %v", err)
	}

	// Everything a sandbox might try. None of it may reach the filesystem as
	// anything other than a single name inside the directory.
	refused := []string{
		"",
		".",
		"..",
		"../deploy",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/deploy",
		"subdir",
		"readme.txt",
		".hidden",
		"deploy ",
		"deploy;rm -rf /",
		"deploy\nnotify",
		strings.Repeat("a", 65),
		"missing",
	}
	for _, name := range refused {
		path, err := Resolve(dir, name)
		if err == nil {
			t.Errorf("Resolve(%q) = %q, want it refused", name, path)
			continue
		}
		if !errors.Is(err, ErrUnknown) {
			t.Errorf("Resolve(%q) = %v, want ErrUnknown", name, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"shell", "#!/bin/sh\n# af: a shell wrapper\n", "a shell wrapper"},
		{"first-line", "# af: right at the top\n", "right at the top"},
		{"slashes", "#!/usr/bin/env node\n// af: a node wrapper\n", "a node wrapper"},
		{"semicolon", "; af: a lisp wrapper\n", "a lisp wrapper"},
		{"indented", "#!/bin/sh\n   #   af:   spaced out   \n", "spaced out"},
		{"none", "#!/bin/sh\necho hello\n", ""},
		{"too-late", "#!/bin/sh\n\n\n\n\n\n\n\n\n\n\n# af: past the first few lines\n", ""},
		{"binary", "\x7fELF\x02\x00# af: not prose\n", ""},
		{"control-characters", "# af: tidy\x07\x08 description\n", "tidy description"},
		{"long", "# af: " + strings.Repeat("x", 500) + "\n", strings.Repeat("x", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeFile(t, dir, tt.name, 0o755, tt.content)
			if got := Describe(filepath.Join(dir, tt.name)); got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListRejectsAMissingDirectory(t *testing.T) {
	t.Parallel()
	if _, err := List(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("List succeeded on a missing directory")
	}
}
