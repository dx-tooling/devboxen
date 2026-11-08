package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	k := Keys{Identity: filepath.Join(dir, "identity.txt"), Recipients: filepath.Join(dir, "recipient.txt")}
	if err := k.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := k.Generate(); err == nil {
		t.Error("overwrote an identity")
	}
	if st, _ := os.Stat(k.Identity); st.Mode().Perm() != 0o600 {
		t.Errorf("identity mode %v", st.Mode().Perm())
	}
	f := filepath.Join(dir, "x.env.age")
	if err := k.Encrypt([]byte("a=b\n"), f); err != nil {
		t.Fatal(err)
	}
	got, err := k.Decrypt(f)
	if err != nil || string(got) != "a=b\n" {
		t.Fatalf("%q %v", got, err)
	}
	// Without a recipients file: the identity's own public key.
	k2 := Keys{Identity: k.Identity}
	if err := k2.Encrypt([]byte("c"), f); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Decrypt(f); string(got) != "c" {
		t.Error("identity recipient")
	}
	if l := Lines([]byte("# c\n\n a=b \nc=d\n")); len(l) != 2 || l[0] != "a=b" {
		t.Errorf("lines: %q", l)
	}
}
