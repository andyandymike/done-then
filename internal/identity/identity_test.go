package identity

import (
	"strings"
	"testing"
)

func TestNewProducesUniqueOpaqueIdentity(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if first.JobID == second.JobID || first.NonceHash == second.NonceHash {
		t.Fatal("New() produced a duplicate identity")
	}
	if !strings.HasPrefix(first.JobID, "dt_") || !strings.HasPrefix(first.NonceHash, "sha256:") {
		t.Fatalf("New() = %#v", first)
	}
}

func TestSHA256DoesNotExposeInput(t *testing.T) {
	value := SHA256([]byte("sensitive prompt"))
	if strings.Contains(value, "sensitive") || len(value) != 64 {
		t.Fatalf("SHA256() = %q", value)
	}
}
