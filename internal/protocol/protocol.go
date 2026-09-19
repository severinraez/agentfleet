// Package protocol defines the agentfleet wire protocol: the messages a
// sandbox and a hub exchange over one websocket connection per call.
//
// Every protocol message is one websocket binary message. The first byte is
// the kind; the rest is the payload — raw bytes for the standard streams,
// JSON for everything else.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Version is the protocol both sides must agree on. A hub rejects any other
// version by name rather than failing in some stranger way later.
const Version = "agentfleet/1"

// Kind is the type tag carried in the first byte of every message.
type Kind byte

const (
	KindHello    Kind = 0x01
	KindStdin    Kind = 0x02
	KindStdinEOF Kind = 0x03

	KindStdout Kind = 0x11
	KindStderr Kind = 0x12
	KindExit   Kind = 0x13
	KindFatal  Kind = 0x14
	KindList   Kind = 0x15
)

func (k Kind) String() string {
	switch k {
	case KindHello:
		return "hello"
	case KindStdin:
		return "stdin"
	case KindStdinEOF:
		return "stdin-eof"
	case KindStdout:
		return "stdout"
	case KindStderr:
		return "stderr"
	case KindExit:
		return "exit"
	case KindFatal:
		return "fatal"
	case KindList:
		return "list"
	default:
		return fmt.Sprintf("unknown(%#x)", byte(k))
	}
}

const (
	// ChunkSize is how much of a standard stream moves in one message.
	ChunkSize = 32 << 10
	// ReadLimit is the largest message either side accepts. It leaves room
	// for a ChunkSize payload and for control messages to grow.
	ReadLimit = 1 << 20
)

// Operations a sandbox can ask for.
const (
	OpExec = "exec"
	OpList = "list"
)

// identRE admits one short, unsurprising identifier: no '/', no leading dot,
// nothing that needs quoting in a log line. Both a sandbox id and a capability
// name must match it — they arrive on the wire, so the rule lives with the
// wire format and both sides enforce the same one.
var identRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidIdent reports whether s is usable as a sandbox id or capability name.
func ValidIdent(s string) bool { return identRE.MatchString(s) }

// ValidateSandboxID checks the id a sandbox announces. A sandbox picks its own
// id, so the hub applies this to what arrives on the wire and the sandbox's own
// config applies it before dialling — one rule and one message, because the
// person who has to fix it reads whichever of the two happens to complain.
func ValidateSandboxID(id string) error {
	if !ValidIdent(id) {
		return fmt.Errorf("sandbox.id %q is not usable: 1-64 characters of letters, digits, dot, dash or underscore, starting with a letter or digit", id)
	}
	return nil
}

// Hello is the first message of every connection.
type Hello struct {
	Protocol string   `json:"protocol"`
	Sandbox  string   `json:"sandbox"`
	Op       string   `json:"op"`
	Name     string   `json:"name,omitempty"`
	Args     []string `json:"args,omitempty"`
}

// Exit reports how the host binary ended. Signal is set instead of Code when
// the binary was killed, and Status turns it into 128+N.
type Exit struct {
	Code   int `json:"code"`
	Signal int `json:"signal,omitempty"`
}

// Status is the exit code this ending becomes: a signalled binary is reported
// as 128+N, the way a shell reports it. The sandbox exits with this and the
// hub logs it, so the rule lives here rather than in each of them.
func (e Exit) Status() int {
	if e.Signal != 0 {
		return 128 + e.Signal
	}
	return e.Code
}

// Fatal is agentfleet itself failing: the sandbox prints Message and exits 125.
type Fatal struct {
	Message string `json:"message"`
}

// Capability is one entry of the listing.
type Capability struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// List answers OpList.
type List struct {
	Capabilities []Capability `json:"capabilities"`
}

// Message is a decoded wire message. Data is the payload without the tag.
type Message struct {
	Kind Kind
	Data []byte
}

// JSON decodes the payload of a control message.
func (m Message) JSON(v any) error {
	if err := json.Unmarshal(m.Data, v); err != nil {
		return fmt.Errorf("malformed %s message: %w", m.Kind, err)
	}
	return nil
}

var errEmptyMessage = errors.New("empty message")

// Encode tags a payload with its kind.
func Encode(kind Kind, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+1)
	out = append(out, byte(kind))
	return append(out, payload...)
}

// EncodeJSON tags a JSON-encoded control message.
func EncodeJSON(kind Kind, v any) ([]byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding %s message: %w", kind, err)
	}
	return Encode(kind, payload), nil
}

// Decode splits a wire message into its kind and payload.
func Decode(b []byte) (Message, error) {
	if len(b) == 0 {
		return Message{}, errEmptyMessage
	}
	return Message{Kind: Kind(b[0]), Data: b[1:]}, nil
}

// ExitAgentfleet is the exit code for agentfleet's own failures — an
// unreachable hub, an unknown capability, a protocol mismatch. Any other code
// the sandbox reports came from the host binary itself.
const ExitAgentfleet = 125
