package mousa

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// TestAccessEventContract pins the identity tuple and the exact sorted-key bytes of one event, and
// the RFC 6962 leaf hash of those bytes.
func TestAccessEventContract(t *testing.T) {
	sourceID, err := NewSourceID("example.source", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewAccessEvent(sourceID, SourceAccessAllow, "mousa-local.cli", 1700000000000000)
	if err != nil {
		t.Fatal(err)
	}
	var time [8]byte
	binary.BigEndian.PutUint64(time[:], 1700000000000000)
	if want := testTupleSHA256([]byte(AccessEventSchema), sourceID[:], []byte("allow"), time[:], []byte("mousa-local.cli")); event.ID != AccessEventID(want) {
		t.Fatal("access event identity does not match the contract field tuple")
	}
	encoded, err := EncodeAccessEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"access":"allow","actor":"mousa-local.cli","id":"` + event.ID.String() + `","occurred_at_usec":1700000000000000,"schema":"mousa.access_event.v1","source_id":"3d2fe6ed8b99d0cadef9f4ff85fb524a7ef5fddd8b249600acb40c7c8c615521"}`
	if string(encoded) != want {
		t.Fatalf("canonical bytes = %s\nwant %s", encoded, want)
	}
	decoded, err := DecodeAccessEvent(encoded)
	if err != nil || decoded != event {
		t.Fatalf("DecodeAccessEvent = %#v, %v", decoded, err)
	}
	for name, data := range map[string]string{
		"trailing newline": want + "\n",
		"unsorted keys":    strings.Replace(want, `{"access":"allow","actor":"mousa-local.cli",`, `{"actor":"mousa-local.cli","access":"allow",`, 1),
		"changed access":   strings.Replace(want, `"allow"`, `"deny"`, 1),
	} {
		if _, err := DecodeAccessEvent([]byte(data)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, actor := range []string{"", "tab\there", "café", strings.Repeat("a", 257)} {
		if _, err := NewAccessEvent(sourceID, SourceAccessDeny, actor, 1); err == nil {
			t.Errorf("actor %q accepted", actor)
		}
	}
	if _, err := NewAccessEvent(sourceID, SourceAccessDeny, "a", 1<<53); err == nil {
		t.Error("time beyond 2^53-1 accepted")
	}
	// RFC 6962 leaf hash of the empty leaf, SHA-256(0x00).
	if got := LogLeafHash(nil); hex.EncodeToString(got[:]) != "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d" {
		t.Fatalf("empty leaf hash = %x", got)
	}
}
