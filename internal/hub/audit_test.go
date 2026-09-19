package hub

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestRecordString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record Record
		want   string
	}{
		{
			// Straight out of the README.
			name: "the quickstart line",
			record: Record{
				SandboxID: "mybox", Name: "deploy",
				Exit: 0, Duration: 1400 * time.Millisecond, In: 0, Out: 2100,
			},
			want: "2026-09-19T14:03:11Z mybox deploy exit=0 1.4s in=0B out=2.1kB",
		},
		{
			name: "the hub section line",
			record: Record{
				SandboxID: "mybox", Name: "deploy", Args: []string{"--force"},
				Exit: 1, Duration: 12700 * time.Millisecond, In: 0, Out: 340,
			},
			want: "2026-09-19T14:03:11Z mybox deploy --force exit=1 12.7s in=0B out=340B",
		},
		{
			name: "a refused call says why",
			record: Record{
				SandboxID: "mybox", Name: "nosuch", Exit: 125, Error: "unknown-capability",
			},
			want: "2026-09-19T14:03:11Z mybox nosuch exit=125 0.0s in=0B out=0B error=unknown-capability",
		},
		{
			name: "a signalled binary is reported as the sandbox saw it",
			record: Record{
				SandboxID: "mybox", Name: "longrun", Exit: 137, Duration: 90 * time.Second, Out: 1_300_000,
			},
			want: "2026-09-19T14:03:11Z mybox longrun exit=137 90.0s in=0B out=1.3MB",
		},
		{
			// An argument cannot forge a second line, however it is spelled.
			name: "awkward arguments are quoted",
			record: Record{
				SandboxID: "mybox", Name: "deploy",
				Args: []string{"--message", "two words", "2026-01-01T00:00:00Z other exit=0", "line\nbreak", ""},
			},
			want: `2026-09-19T14:03:11Z mybox deploy --message "two words" "2026-01-01T00:00:00Z other exit=0" "line\nbreak" "" exit=0 0.0s in=0B out=0B`,
		},
		{
			name:   "a hostile sandbox id is quoted too",
			record: Record{SandboxID: "box one", Name: "deploy"},
			want:   `2026-09-19T14:03:11Z "box one" deploy exit=0 0.0s in=0B out=0B`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.record
			r.Time = at(t, "2026-09-19T14:03:11Z")
			if got := r.String(); got != tt.want {
				t.Errorf("\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestRecordTimeIsUTC(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC+5", 5*60*60)
	r := Record{Time: at(t, "2026-09-19T14:03:11Z").In(zone), SandboxID: "mybox", Name: "deploy"}
	if !strings.HasPrefix(r.String(), "2026-09-19T14:03:11Z ") {
		t.Errorf("line = %s, want a UTC timestamp", r.String())
	}
}

func TestBytesOf(t *testing.T) {
	t.Parallel()
	tests := map[int64]string{
		0: "0B", 1: "1B", 340: "340B", 999: "999B",
		1000: "1.0kB", 2100: "2.1kB", 999_999: "1000.0kB",
		1_300_000: "1.3MB", 5 << 20: "5.2MB", 3_000_000_000: "3.0GB",
	}
	for n, want := range tests {
		if got := bytesOf(n); got != want {
			t.Errorf("bytesOf(%d) = %s, want %s", n, got, want)
		}
	}
}

// Concurrent calls must not interleave halfway through a record.
func TestAuditLogWritesWholeLines(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	log := &auditLog{w: &buf}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.write(Record{Time: time.Now(), SandboxID: "mybox", Name: "deploy"})
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines, want 50", len(lines))
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, "mybox deploy exit=0 0.0s in=0B out=0B") {
			t.Fatalf("torn line: %q", line)
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Starting up is news for whoever is watching the log, so it belongs on the
// same stream as the calls rather than among the diagnostics.
func TestListenAndServeAnnouncesOnTheLogStream(t *testing.T) {
	t.Parallel()

	var out, diagnostics syncBuffer
	h := &Hub{Directory: t.TempDir(), Audit: &out, Errors: &diagnostics}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.ListenAndServe(ctx, "127.0.0.1:0") }()

	announced := func() bool { return strings.Contains(out.String(), "listening on") }
	deadline := time.Now().Add(5 * time.Second)
	for !announced() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Re-check after the deadline: on a loaded machine the last sleep can
	// outlast it even though the hub did announce itself.
	if !announced() {
		t.Error("the hub did not announce itself within 5s")
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	line := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(line, "agentfleet hub: listening on 127.0.0.1:") {
		t.Errorf("stdout = %q", line)
	}
	if !strings.HasSuffix(line, "capabilities from "+h.Directory) {
		t.Errorf("stdout = %q, want it to name the rpc directory", line)
	}
	if diagnostics.String() != "" {
		t.Errorf("stderr = %q, want nothing", diagnostics.String())
	}
}
