package confbackup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Sealed is the encrypted secrets section: AES-256-GCM with a key derived
// from the passphrase by Argon2id. Salt and nonce are random per export; the
// parameters travel with the file so they can be raised later.
type Sealed struct {
	Cipher  string `json:"cipher"` // aes-256-gcm
	KDF     string `json:"kdf"`    // argon2id
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

// Argon2id cost: 64 MiB and 3 passes, about a second on a small NAS.
const (
	kdfTime    = 3
	kdfMemory  = 64 * 1024
	kdfThreads = 4
	// MinPassphrase is the shortest passphrase accepted for an export.
	MinPassphrase = 8
)

// aad binds the ciphertext to the document format.
var aad = []byte(Format + "/secrets")

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Seal encrypts v (encoded as JSON) with the passphrase.
func Seal(v any, passphrase string) (*Sealed, error) {
	if len(passphrase) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := &Sealed{Cipher: "aes-256-gcm", KDF: "argon2id", Time: kdfTime, Memory: kdfMemory, Threads: kdfThreads, Salt: make([]byte, 16), Nonce: make([]byte, 12)}
	if _, err := rand.Read(s.Salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(s.Nonce); err != nil {
		return nil, err
	}
	a, err := gcm(argon2.IDKey([]byte(passphrase), s.Salt, s.Time, s.Memory, s.Threads, 32))
	if err != nil {
		return nil, err
	}
	s.Data = a.Seal(nil, s.Nonce, plain, aad)
	return s, nil
}

// Open decrypts the section into v; ErrPassphrase when the passphrase (or
// the file) is wrong.
func (s *Sealed) Open(passphrase string, v any) error {
	if s.Cipher != "aes-256-gcm" || s.KDF != "argon2id" {
		return errors.New("unknown encryption of the secrets")
	}
	// The cost comes from the file: bound it so a crafted file cannot
	// exhaust the memory of the NAS.
	if s.Time < 1 || s.Time > 16 || s.Memory < 8*1024 || s.Memory > 512*1024 || s.Threads < 1 || s.Threads > 16 || len(s.Salt) < 16 || len(s.Nonce) != 12 {
		return errors.New("invalid encryption parameters")
	}
	a, err := gcm(argon2.IDKey([]byte(passphrase), s.Salt, s.Time, s.Memory, s.Threads, 32))
	if err != nil {
		return err
	}
	plain, err := a.Open(nil, s.Nonce, s.Data, aad)
	if err != nil {
		return ErrPassphrase
	}
	return json.Unmarshal(plain, v)
}
