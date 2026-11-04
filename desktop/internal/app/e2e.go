package app

import (
	"context"
	"encoding/base64"
	"errors"
	"log"

	"strataos-sync/internal/api"
	"strataos-sync/internal/config"
	"strataos-sync/internal/e2e"
)

// PairOptions: end-to-end encryption of a new folder. Encrypt makes an
// empty NAS folder encrypted with Passphrase; a folder already encrypted
// is unlocked with Passphrase or RecoveryKey.
type PairOptions struct {
	Encrypt     bool   `json:"encrypt"`
	Passphrase  string `json:"passphrase"`
	RecoveryKey string `json:"recoveryKey"`
}

// e2eLocked is answered when an encrypted folder needs its passphrase.
type e2eLocked struct{ msg string }

func (e e2eLocked) Error() string { return e.msg }

// IsE2ELocked tells whether err asks for the passphrase of a folder.
func IsE2ELocked(err error) bool {
	var l e2eLocked
	return errors.As(err, &l)
}

// setupE2E returns the key of the NAS folder remote when it is (or becomes)
// encrypted, nil for a plain folder.
func (a *App) setupE2E(ctx context.Context, c *api.Client, remote string, opt PairOptions) (*config.E2EKey, error) {
	st, err := c.E2E(ctx, remote)
	if err != nil {
		if api.StatusOf(err) == 404 && !opt.Encrypt {
			return nil, nil // a NAS before 0.10: no encrypted folders there
		}
		return nil, err
	}
	switch {
	case st.Encrypted && st.Root != remote:
		return nil, errors.New(a.T("err.e2e.inside", st.Root))
	case st.Encrypted:
		if st.Header == nil {
			return nil, errors.New(a.T("err.e2e.header"))
		}
		var mk []byte
		switch {
		case opt.RecoveryKey != "":
			if mk, err = e2e.ParseRecoveryKey(opt.RecoveryKey); err == nil {
				err = e2e.Verify(*st.Header, mk)
			}
			if err != nil {
				return nil, errors.New(a.T("err.e2e.recovery"))
			}
		case opt.Passphrase != "":
			if mk, err = e2e.Unlock(*st.Header, opt.Passphrase); err != nil {
				if errors.Is(err, e2e.ErrPassphrase) {
					return nil, errors.New(a.T("err.e2e.passphrase"))
				}
				return nil, err
			}
		default:
			return nil, e2eLocked{a.T("err.e2e.locked")}
		}
		return &config.E2EKey{ID: st.Header.ID, Key: base64.StdEncoding.EncodeToString(mk)}, nil
	case opt.Encrypt:
		if len([]rune(opt.Passphrase)) < e2e.MinPassphrase {
			return nil, errors.New(a.T("err.e2e.short", e2e.MinPassphrase))
		}
		h, mk, err := e2e.New(opt.Passphrase)
		if err != nil {
			return nil, err
		}
		if err := c.CreateE2E(ctx, remote, h); err != nil {
			return nil, err
		}
		return &config.E2EKey{ID: h.ID, Key: base64.StdEncoding.EncodeToString(mk)}, nil
	}
	return nil, nil
}

// e2eKeysLocked gives the engine the keys of the encrypted folders (caller holds mu).
func (a *App) e2eKeysLocked() {
	if a.e2e == nil {
		return
	}
	for _, p := range a.cfg.Pairs {
		k, ok := a.cfg.E2E[p.ID]
		if !p.Encrypted || !ok {
			a.e2e.SetFolder(p.Remote, "", nil)
			continue
		}
		mk, err := base64.StdEncoding.DecodeString(k.Key)
		keys, kerr := e2e.Derive(mk)
		if err != nil || kerr != nil {
			log.Printf("encrypted folder %s: bad key in the settings", p.Remote)
			continue
		}
		a.e2e.SetFolder(p.Remote, k.ID, keys)
	}
}

func (a *App) e2eSkipped(folder, path string) {
	log.Printf("encrypted folder %s: %s is not encrypted with its key (left out)", folder, path)
}

func (a *App) pairKey(id string) (string, config.E2EKey, []byte, error) {
	c := a.Config()
	for _, p := range c.Pairs {
		if p.ID == id {
			k, ok := c.E2E[id]
			if !p.Encrypted || !ok {
				return "", k, nil, errors.New(a.T("err.e2e.notencrypted"))
			}
			mk, err := base64.StdEncoding.DecodeString(k.Key)
			return p.Remote, k, mk, err
		}
	}
	return "", config.E2EKey{}, nil, errors.New("folder not found")
}

// RecoveryKey returns the recovery key of an encrypted folder.
func (a *App) RecoveryKey(id string) (string, error) {
	_, _, mk, err := a.pairKey(id)
	if err != nil {
		return "", err
	}
	return e2e.RecoveryKey(mk), nil
}

// ChangePassphrase seals the key of an encrypted folder with a new
// passphrase (the other computers keep working; new ones use the new one).
func (a *App) ChangePassphrase(ctx context.Context, id, pass string) error {
	remote, k, mk, err := a.pairKey(id)
	if err != nil {
		return err
	}
	c := a.Client()
	if c == nil {
		return errors.New(a.T("err.notpaired"))
	}
	st, err := c.E2E(ctx, remote)
	if err != nil {
		return err
	}
	if !st.Encrypted || st.Header == nil || st.Header.ID != k.ID {
		return errors.New(a.T("err.e2e.header"))
	}
	if len([]rune(pass)) < e2e.MinPassphrase {
		return errors.New(a.T("err.e2e.short", e2e.MinPassphrase))
	}
	h, err := e2e.Rewrap(*st.Header, mk, pass)
	if err != nil {
		return err
	}
	return c.UpdateE2E(ctx, remote, h)
}

// RedactKeys removes the folder keys from a configuration shown to the UI.
func RedactKeys(c config.Config) config.Config {
	c.E2E = nil
	return c
}
