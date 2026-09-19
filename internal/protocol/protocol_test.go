package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeDecodeStream(t *testing.T) {
	t.Parallel()

	payloads := [][]byte{nil, {}, []byte("hello"), bytes.Repeat([]byte{0xff}, ChunkSize)}
	for _, payload := range payloads {
		msg, err := Decode(Encode(KindStdout, payload))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if msg.Kind != KindStdout {
			t.Errorf("kind = %v, want stdout", msg.Kind)
		}
		if !bytes.Equal(msg.Data, payload) {
			t.Errorf("payload round-tripped as %q", msg.Data)
		}
	}
}

func TestEncodeDecodeControl(t *testing.T) {
	t.Parallel()

	hello := Hello{Protocol: Version, Sandbox: "mybox", Op: OpExec, Name: "deploy", Args: []string{"--force", "a b"}}
	raw, err := EncodeJSON(KindHello, hello)
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	msg, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if msg.Kind != KindHello {
		t.Fatalf("kind = %v, want hello", msg.Kind)
	}
	var got Hello
	if err := msg.JSON(&got); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got.Sandbox != hello.Sandbox || got.Name != hello.Name || len(got.Args) != 2 || got.Args[1] != "a b" {
		t.Errorf("hello round-tripped as %+v", got)
	}
}

func TestDecodeRejectsAnEmptyMessage(t *testing.T) {
	t.Parallel()
	if _, err := Decode(nil); err == nil {
		t.Fatal("Decode(nil) succeeded")
	}
}

func TestJSONReportsTheKind(t *testing.T) {
	t.Parallel()
	msg := Message{Kind: KindExit, Data: []byte("not json")}
	var exit Exit
	err := msg.JSON(&exit)
	if err == nil {
		t.Fatal("JSON succeeded on garbage")
	}
	if want := "malformed exit message"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to start with %q", err, want)
	}
}

func TestKindString(t *testing.T) {
	t.Parallel()
	if got := KindStdinEOF.String(); got != "stdin-eof" {
		t.Errorf("KindStdinEOF = %q", got)
	}
	if got := Kind(0x7f).String(); got != "unknown(0x7f)" {
		t.Errorf("unknown kind = %q", got)
	}
}
