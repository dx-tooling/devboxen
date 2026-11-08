// Package secrets encrypts and decrypts the instance's secrets with age: a private key
// (identity) that never leaves your machine, and its public key (recipient) to encrypt.
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// Keys are the paths of the identity and the recipients file.
type Keys struct {
	Identity   string
	Recipients string
}

// IdentityPath: $DEVBOXEN_AGE_IDENTITY, else $SECRETS_AGE_KEY_FILE, else the configured one.
func IdentityPath(configured string) string {
	for _, env := range []string{"DEVBOXEN_AGE_IDENTITY", "SECRETS_AGE_KEY_FILE"} {
		if p := os.Getenv(env); p != "" {
			return p
		}
	}
	return configured
}

func (k Keys) identities() ([]age.Identity, error) {
	if k.Identity == "" {
		return nil, errors.New("no age identity configured (secrets.identity)")
	}
	f, err := os.Open(k.Identity)
	if err != nil {
		return nil, fmt.Errorf("the age identity (private key): %w", err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.Identity, err)
	}
	return ids, nil
}

func (k Keys) recipients() ([]age.Recipient, error) {
	if k.Recipients != "" {
		if data, err := os.ReadFile(k.Recipients); err == nil {
			rs, err := age.ParseRecipients(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k.Recipients, err)
			}
			return rs, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	// No recipients file: the identity's own public key.
	ids, err := k.identities()
	if err != nil {
		return nil, err
	}
	var rs []age.Recipient
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			rs = append(rs, x.Recipient())
		}
	}
	if len(rs) == 0 {
		return nil, errors.New("no recipient to encrypt to (secrets.recipients)")
	}
	return rs, nil
}

// Decrypt reads an encrypted file.
func (k Keys) Decrypt(path string) ([]byte, error) {
	ids, err := k.identities()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return io.ReadAll(r)
}

// Encrypt writes data encrypted to path (atomically).
func (k Keys) Encrypt(data []byte, path string) error {
	rs, err := k.recipients()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Generate makes a new identity at k.Identity (refusing to overwrite one) and writes its
// public key to k.Recipients.
func (k Keys) Generate() error {
	if _, err := os.Stat(k.Identity); err == nil {
		return fmt.Errorf("%s exists already", k.Identity)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.Identity), 0o700); err != nil {
		return err
	}
	content := "# devboxen: the age private key of this instance's secrets. Keep it safe; never commit it.\n" + id.String() + "\n"
	if err := os.WriteFile(k.Identity, []byte(content), 0o600); err != nil {
		return err
	}
	if k.Recipients != "" {
		return os.WriteFile(k.Recipients, []byte(id.Recipient().String()+"\n"), 0o644)
	}
	return nil
}

// Lines splits a plaintext into its non-empty, non-comment lines.
func Lines(data []byte) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
