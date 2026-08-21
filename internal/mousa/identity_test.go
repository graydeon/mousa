package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDocumentIDMatchesCanonicalVector(t *testing.T) {
	digestBytes, err := hex.DecodeString("e9024f1a07d29d52ad3aa5e1a18e94db1f3a9fd32b89e39d47c472cd99071e13")
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	copy(digest[:], digestBytes)

	got := NewDocumentID("source:a", SHA256(digest))
	const want = "70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3"
	if got.String() != want {
		t.Fatalf("NewDocumentID() = %q, want %q", got.String(), want)
	}
}

func TestDocumentIDStrictParsingAndJSON(t *testing.T) {
	const valid = "70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3"
	id, err := ParseDocumentID(valid)
	if err != nil {
		t.Fatalf("ParseDocumentID(valid): %v", err)
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	if string(encoded) != `"`+valid+`"` {
		t.Fatalf("json.Marshal() = %s", encoded)
	}

	var decoded DocumentID
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(valid): %v", err)
	}
	if decoded != id {
		t.Fatalf("decoded ID = %s, want %s", decoded, id)
	}

	for _, input := range []string{
		strings.ToUpper(valid),
		valid[:63],
		valid + "0",
		strings.Repeat("g", 64),
	} {
		t.Run(input, func(t *testing.T) {
			_, err := ParseDocumentID(input)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if validationErr.Field != "id" || validationErr.Code != ValidationCodeInvalidID {
				t.Fatalf("validation evidence = %q/%q, want id/%q", validationErr.Field, validationErr.Code, ValidationCodeInvalidID)
			}
		})
	}

	if err := json.Unmarshal([]byte(`"`+strings.ToUpper(valid)+`"`), &decoded); err == nil {
		t.Fatal("json.Unmarshal(uppercase) succeeded")
	}
}

func TestChunkIDMatchesCanonicalVector(t *testing.T) {
	documentID, err := ParseDocumentID("70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3")
	if err != nil {
		t.Fatal(err)
	}
	digestBytes, err := hex.DecodeString("1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1")
	if err != nil {
		t.Fatal(err)
	}
	var digest SHA256
	copy(digest[:], digestBytes)

	got, err := NewChunkID(documentID, 3, 12, 22, digest)
	if err != nil {
		t.Fatalf("NewChunkID(): %v", err)
	}
	const want = "14d47ddbacb490a34c4223517eb4b841546d8c6980b4116198fdf0c1ff4529cf"
	if got.String() != want {
		t.Fatalf("NewChunkID() = %q, want %q", got.String(), want)
	}
}

func TestChunkIDRejectsInvalidRange(t *testing.T) {
	_, err := NewChunkID(DocumentID{}, 0, 2, 1, SHA256{})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Field != "end_byte" || validationErr.Code != ValidationCodeInvalidRange {
		t.Fatalf("validation evidence = %q/%q, want end_byte/%q", validationErr.Field, validationErr.Code, ValidationCodeInvalidRange)
	}
}

func TestSHA256StrictParsingAndJSON(t *testing.T) {
	const valid = "1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1"
	digest, err := ParseSHA256(valid)
	if err != nil {
		t.Fatalf("ParseSHA256(valid): %v", err)
	}
	encoded, err := json.Marshal(digest)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	if string(encoded) != `"`+valid+`"` {
		t.Fatalf("json.Marshal() = %s", encoded)
	}

	for _, input := range []string{strings.ToUpper(valid), valid[:63], valid + "0", strings.Repeat("g", 64)} {
		_, err := ParseSHA256(input)
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Field != "sha256" || validationErr.Code != ValidationCodeInvalidDigest {
			t.Fatalf("ParseSHA256(%q) error = %#v, want sha256/%q", input, validationErr, ValidationCodeInvalidDigest)
		}
	}

	var decoded SHA256
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != digest {
		t.Fatalf("json.Unmarshal(valid) = %s, %v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`123`), &decoded); err == nil {
		t.Fatal("json.Unmarshal(number) succeeded")
	}
}

func TestChunkIDStrictParsingAndJSON(t *testing.T) {
	const valid = "14d47ddbacb490a34c4223517eb4b841546d8c6980b4116198fdf0c1ff4529cf"
	id, err := ParseChunkID(valid)
	if err != nil {
		t.Fatalf("ParseChunkID(valid): %v", err)
	}
	encoded, err := json.Marshal(id)
	if err != nil || string(encoded) != `"`+valid+`"` {
		t.Fatalf("json.Marshal() = %s, %v", encoded, err)
	}
	var decoded ChunkID
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != id {
		t.Fatalf("json.Unmarshal(valid) = %s, %v", decoded, err)
	}
	for _, input := range []string{strings.ToUpper(valid), valid[:63], valid + "0", strings.Repeat("g", 64)} {
		_, err := ParseChunkID(input)
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Field != "id" || validationErr.Code != ValidationCodeInvalidID {
			t.Fatalf("ParseChunkID(%q) error = %#v, want id/%q", input, validationErr, ValidationCodeInvalidID)
		}
	}
}

func TestDocumentIDAndChunkIDAreDeterministic(t *testing.T) {
	digest, err := ParseSHA256("e9024f1a07d29d52ad3aa5e1a18e94db1f3a9fd32b89e39d47c472cd99071e13")
	if err != nil {
		t.Fatal(err)
	}
	firstDocument := NewDocumentID("source:a", digest)
	secondDocument := NewDocumentID("source:a", digest)
	if firstDocument != secondDocument {
		t.Fatal("repeated document identity generation differed")
	}
	firstChunk, err := NewChunkID(firstDocument, 3, 12, 22, digest)
	if err != nil {
		t.Fatal(err)
	}
	secondChunk, err := NewChunkID(firstDocument, 3, 12, 22, digest)
	if err != nil {
		t.Fatal(err)
	}
	if firstChunk != secondChunk {
		t.Fatal("repeated chunk identity generation differed")
	}
}

func TestDocumentIDTupleEncodingDistinguishesFieldBoundaries(t *testing.T) {
	left := sha256.New()
	writeTuple(left, []byte("ab"), []byte("c"))
	right := sha256.New()
	writeTuple(right, []byte("a"), []byte("bc"))
	if string(left.Sum(nil)) == string(right.Sum(nil)) {
		t.Fatal("length-prefixed tuple encoding collided across field boundaries")
	}
}
