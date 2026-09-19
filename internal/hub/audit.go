package hub

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/severinraez/agentfleet/internal/protocol"
)

// Record is one line of the hub's log: the whole logging story, per call.
//
//	2026-09-19T14:03:11Z mybox deploy --force exit=1 12.7s in=0B out=340B
//
// Timestamp, sandbox id, binary, arguments, exit code, duration, and bytes
// moved each way. Calls the hub refuses carry a trailing error= instead of
// going unrecorded — a denied attempt is exactly what a log like this is for.
type Record struct {
	Time      time.Time
	SandboxID string
	Name      string
	Args      []string
	Exit      int
	Duration  time.Duration
	In        int64
	Out       int64
	Error     string
}

// newRecord starts the log line for one call. Everything known before the
// capability runs is filled in here, so that a refused call and a completed
// one cannot describe themselves differently.
func newRecord(hello protocol.Hello, started time.Time) Record {
	return Record{
		Time:      started,
		SandboxID: hello.Sandbox,
		Name:      hello.Name,
		Args:      hello.Args,
		Exit:      protocol.ExitAgentfleet,
	}
}

func (r Record) String() string {
	var b strings.Builder
	b.WriteString(r.Time.UTC().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(field(r.SandboxID))
	b.WriteByte(' ')
	b.WriteString(field(r.Name))
	for _, a := range r.Args {
		b.WriteByte(' ')
		b.WriteString(field(a))
	}
	fmt.Fprintf(&b, " exit=%d %.1fs in=%s out=%s", r.Exit, r.Duration.Seconds(), bytesOf(r.In), bytesOf(r.Out))
	if r.Error != "" {
		b.WriteString(" error=")
		b.WriteString(field(r.Error))
	}
	return b.String()
}

const safeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/:=@%+,-"

// field prints a value as-is when it is unambiguous and quotes it otherwise.
// Ordinary arguments come out exactly as they were typed, while a space or a
// newline in one cannot forge a second log line.
func field(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if !strings.ContainsRune(safeChars, r) {
			return strconv.Quote(s)
		}
	}
	return s
}

var units = []string{"kB", "MB", "GB", "TB", "PB"}

// bytesOf formats a byte count the way the README shows it: 0B, 340B, 2.1kB.
func bytesOf(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%dB", n)
	}
	v, i := float64(n)/1000, 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

// auditLog writes whole lines, one Write each, so concurrent calls cannot
// interleave halfway through a record.
type auditLog struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *auditLog) write(r Record) {
	l.line(r.String())
}

// line writes one line to the same stream the records go to, for the hub's own
// announcements. It takes the same lock, so an announcement cannot land in the
// middle of a record.
func (l *auditLog) line(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(l.w, s+"\n")
}
