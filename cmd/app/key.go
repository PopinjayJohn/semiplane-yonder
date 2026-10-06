package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/semiplane/yonder/internal/auth"
)

// Session-key file handling (ops side). The key material itself comes from
// auth.GenerateKey (Lane C provides, E1 wires into init/serve/rotate per the
// lane briefs). This file only handles the 0600 file mechanics; it performs
// no signing, session, or cookie logic.

// ensureSessionKey returns the path to <data-dir>/.sessionkey, generating it
// via auth.GenerateKey when missing. Existing keys are never overwritten.
// The key is never logged.
func ensureSessionKey(dataDir string) (string, error) {
	if err := ensureDir(dataDir); err != nil {
		return "", err
	}
	keyPath := filepath.Join(dataDir, ".sessionkey")
	if _, err := os.Stat(keyPath); err == nil {
		return keyPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	key, err := auth.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate session key: %w", err)
	}
	if err := writeFileAtomic(keyPath, key[:], 0o600); err != nil {
		return "", err
	}
	return keyPath, nil
}

// rotateSessionKeyFile replaces <data-dir>/.sessionkey with a fresh
// auth.GenerateKey value (0600, atomic). All sessions signed by the old key
// stop verifying — callers must report that.
func rotateSessionKeyFile(dataDir string) (string, error) {
	if err := ensureDir(dataDir); err != nil {
		return "", err
	}
	keyPath := filepath.Join(dataDir, ".sessionkey")
	key, err := auth.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate session key: %w", err)
	}
	if err := writeFileAtomic(keyPath, key[:], 0o600); err != nil {
		return "", err
	}
	return keyPath, nil
}

// writeFileAtomic writes data to path via temp file + rename with the given
// mode. Same pattern vault.WriteFile (Lane B) uses; this copy serves files
// outside the vault (session key, CLI outputs) that Lane B never handles.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
