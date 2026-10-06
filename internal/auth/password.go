package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (spec §7): 64MB memory, 3 iterations, 4 lanes,
// 16-byte salt, 32-byte key. Stored as PHC strings, rehashed on login when
// parameters change.
const (
	ArgonMemory  uint32 = 65536
	ArgonTime    uint32 = 3
	ArgonThreads uint8  = 4
	ArgonSaltLen uint32 = 16
	ArgonKeyLen  uint32 = 32
)

// MaxPasswordBytes bounds a single password before hashing. Argon2 accepts
// arbitrary lengths; the cap keeps one request's hashing cost predictable.
const MaxPasswordBytes = 1024

// ArgonParams captures the argon2id parameters for one hash.
type ArgonParams struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultArgonParams returns the spec §7 parameters.
func DefaultArgonParams() ArgonParams {
	return ArgonParams{
		Memory:  ArgonMemory,
		Time:    ArgonTime,
		Threads: ArgonThreads,
		SaltLen: ArgonSaltLen,
		KeyLen:  ArgonKeyLen,
	}
}

// ErrInvalidPasswordHash is returned when a stored hash is not a parseable
// argon2id PHC string.
var ErrInvalidPasswordHash = errors.New("auth: invalid password hash")

// HashPassword hashes a password with the default argon2id parameters and
// returns the PHC-encoded string.
func HashPassword(password string) (string, error) {
	return hashPasswordWithParams(password, DefaultArgonParams())
}

func hashPasswordWithParams(password string, p ArgonParams) (string, error) {
	if len(password) == 0 {
		return "", errors.New("auth: password must not be empty")
	}
	if len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("auth: password exceeds %d bytes", MaxPasswordBytes)
	}
	if p.SaltLen == 0 || p.KeyLen == 0 || p.Threads == 0 {
		return "", errors.New("auth: invalid argon2 parameters")
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt generation: %w", err)
	}
	key := argon2.Key([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC-encoded argon2id hash.
//
// A nil error means the password matches; needsRehash is then true when the
// stored parameters differ from the current defaults (caller should rehash
// on login). A wrong password returns ErrInvalidCredentials; a malformed
// stored hash returns ErrInvalidPasswordHash. The two are distinct types so
// callers can log storage corruption without leaking which accounts fail.
func VerifyPassword(password, encoded string) (needsRehash bool, err error) {
	p, salt, key, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > MaxPasswordBytes {
		return false, ErrInvalidCredentials
	}
	derived := argon2.Key([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key)))
	if subtle.ConstantTimeCompare(derived, key) != 1 {
		return false, ErrInvalidCredentials
	}
	want := DefaultArgonParams()
	return p != want || uint32(len(key)) != want.KeyLen || uint32(len(salt)) != want.SaltLen, nil
}

func parsePHC(encoded string) (ArgonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	var p ArgonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))
	if len(salt) == 0 || len(key) == 0 || p.Threads == 0 {
		return ArgonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return p, salt, key, nil
}
