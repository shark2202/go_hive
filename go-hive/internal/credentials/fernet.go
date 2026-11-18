package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const fernetVersion = 0x80

type Fernet struct {
	signingKey []byte
	encKey     []byte
}

func NewFernet(key string) (*Fernet, error) {
	trimmed := strings.TrimSpace(key)
	raw, err := base64.URLEncoding.DecodeString(trimmed)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(trimmed, "="))
		if err != nil {
			return nil, fmt.Errorf("invalid fernet key: %w", err)
		}
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("fernet key must decode to 32 bytes, got %d", len(raw))
	}
	return &Fernet{signingKey: raw[:16], encKey: raw[16:]}, nil
}

func GenerateFernetKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate fernet key: %w", err)
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

func (f *Fernet) Encrypt(plaintext []byte) (string, error) {
	return f.EncryptAt(time.Now().UTC(), plaintext)
}

func (f *Fernet) EncryptAt(now time.Time, plaintext []byte) (string, error) {
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("generate iv: %w", err)
	}

	block, err := aes.NewCipher(f.encKey)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	token := make([]byte, 1+8+aes.BlockSize+len(ciphertext))
	token[0] = fernetVersion
	binary.BigEndian.PutUint64(token[1:9], uint64(now.Unix()))
	copy(token[9:9+aes.BlockSize], iv)
	copy(token[9+aes.BlockSize:], ciphertext)

	mac := hmac.New(sha256.New, f.signingKey)
	mac.Write(token)
	token = append(token, mac.Sum(nil)...)
	return base64.URLEncoding.EncodeToString(token), nil
}

func (f *Fernet) Decrypt(token string) ([]byte, error) {
	return f.DecryptTTL(token, 0)
}

func (f *Fernet) DecryptTTL(token string, ttl time.Duration) ([]byte, error) {
	return f.decryptAt(time.Now().UTC(), token, ttl)
}

func (f *Fernet) decryptAt(now time.Time, token string, ttl time.Duration) ([]byte, error) {
	raw, err := base64.URLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(token), "="))
		if err != nil {
			return nil, fmt.Errorf("invalid token: %w", err)
		}
	}

	const overhead = 1 + 8 + aes.BlockSize + sha256.Size
	if len(raw) < overhead+aes.BlockSize {
		return nil, errors.New("invalid token: too short")
	}
	if raw[0] != fernetVersion {
		return nil, fmt.Errorf("invalid token: unsupported version 0x%02x", raw[0])
	}

	signed := raw[:len(raw)-sha256.Size]
	signature := raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, f.signingKey)
	mac.Write(signed)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return nil, errors.New("invalid token: signature mismatch")
	}

	timestamp := int64(binary.BigEndian.Uint64(signed[1:9]))
	if ttl > 0 && now.Sub(time.Unix(timestamp, 0)) > ttl {
		return nil, errors.New("token has expired")
	}

	iv := signed[9 : 9+aes.BlockSize]
	ciphertext := signed[9+aes.BlockSize:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("invalid token: bad ciphertext length")
	}

	block, err := aes.NewCipher(f.encKey)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padding: bad block size")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid padding")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid padding")
		}
	}
	return data[:len(data)-pad], nil
}
