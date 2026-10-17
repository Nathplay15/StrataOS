// Package crypt keeps shared folders encrypted with gocryptfs: the files are
// stored encrypted in a hidden folder of the volume and appear in clear at
// the share path only while it is unlocked with its passphrase (never stored).
package crypt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Dir is the hidden folder of a volume holding the encrypted data.
const Dir = ".strata-crypt"

// Installed reports whether gocryptfs is available.
func Installed() bool { return sys.Has("gocryptfs") }

// CipherDir returns where the encrypted data of a share folder lives.
func CipherDir(volume, sharePath string) (string, error) {
	rel, err := filepath.Rel(volume, sharePath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("the share must be inside a volume")
	}
	return filepath.Join(volume, Dir, strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")), nil
}

func checkPass(p string) error {
	if len(p) < 8 {
		return fmt.Errorf("the passphrase must have at least 8 characters")
	}
	if strings.ContainsAny(p, "\n\r") {
		return fmt.Errorf("invalid passphrase")
	}
	return nil
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// normKey turns a recovery key typed with or without dashes into 64 hex digits.
func normKey(k string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "", "\n", "", "\t", "").Replace(k))
}

// formatKey writes a master key as 8 groups of 8 digits.
func formatKey(hex string) string {
	var g []string
	for i := 0; i+8 <= len(hex); i += 8 {
		g = append(g, hex[i:i+8])
	}
	return strings.Join(g, "-")
}

// Init creates the encrypted store and returns its recovery key (the master
// key, shown once).
func Init(cipher, pass string) (string, error) {
	if err := checkPass(pass); err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(cipher); err == nil && len(ents) > 0 {
		return "", fmt.Errorf("%s is not empty", cipher)
	}
	if err := os.MkdirAll(cipher, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Dir(cipher), 0o700)
	if _, err := sys.RunInput(pass+"\n", "gocryptfs", "-init", "-q", "--", cipher); err != nil {
		return "", err
	}
	// gocryptfs shows the master key only on a terminal: read it back.
	out, err := sys.RunInput(pass+"\n", "gocryptfs-xray", "-dumpmasterkey", filepath.Join(cipher, "gocryptfs.conf"))
	key := normKey(out)
	if err != nil || !hexRe.MatchString(key) {
		return "", fmt.Errorf("cannot read the recovery key")
	}
	return formatKey(key), nil
}

// Mounted reports whether the clear view is mounted at mp.
func Mounted(mp string) bool {
	out, _ := sys.Run("findmnt", "-n", "-o", "FSTYPE", "--mountpoint", mp)
	return strings.TrimSpace(out) == "fuse.gocryptfs"
}

// Unlock mounts the clear view with the passphrase, or with the recovery key
// when recovery is true.
func Unlock(cipher, mp, secret string, recovery bool) error {
	if Mounted(mp) {
		return nil
	}
	if err := os.MkdirAll(mp, 0o770); err != nil {
		return err
	}
	if ents, _ := os.ReadDir(mp); len(ents) > 0 {
		return fmt.Errorf("%s must be empty to be unlocked", mp)
	}
	args := []string{"-q", "-allow_other", "-acl", "-noprealloc"}
	if recovery {
		k := normKey(secret)
		if !hexRe.MatchString(k) {
			return fmt.Errorf("invalid recovery key")
		}
		secret = formatKey(k)
		args = append(args, "-masterkey=stdin")
	}
	args = append(args, "--", cipher, mp)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := sys.RunOpts(ctx, sys.Opts{Stdin: strings.TrimSpace(secret) + "\n"}, "gocryptfs", args...); err != nil {
		if strings.Contains(err.Error(), "exit status 12") || strings.Contains(strings.ToLower(err.Error()), "password incorrect") {
			return fmt.Errorf("wrong passphrase")
		}
		return err
	}
	return nil
}

// Lock unmounts the clear view (files open over the network are closed first
// by the caller).
func Lock(mp string) error {
	if !Mounted(mp) {
		return nil
	}
	if _, err := sys.Run("fusermount3", "-u", mp); err != nil {
		if _, err2 := sys.Run("umount", mp); err2 != nil {
			return fmt.Errorf("the folder is in use: %w", err)
		}
	}
	return nil
}
