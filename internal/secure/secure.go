// Package secure provides at-rest encryption for sensitive values (cookies,
// push tokens) and random token generation for sessions.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Box encrypts and decrypts small secrets with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// New loads (or creates) the local key file inside dataDir.
func New(dataDir string) (*Box, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dataDir, "secret.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		// Write through a temp file + rename so an interrupted first run can
		// never leave a truncated (zero-length) key behind: every encrypted
		// cookie would become unreadable.
		tmp := keyPath + ".tmp"
		if err := os.WriteFile(tmp, key, 0o600); err != nil {
			return nil, fmt.Errorf("写入密钥文件失败: %w", err)
		}
		if err := os.Rename(tmp, keyPath); err != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("保存密钥文件失败: %w", err)
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("密钥文件损坏（长度 %d）。删除 %s 可以重新开始，但所有已加密的 Cookie 将无法恢复，所有账号需要重新登录", len(key), keyPath)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce||ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open reverses Seal.
func (b *Box) Open(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	ns := b.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("密文长度异常")
	}
	return b.aead.Open(nil, data[:ns], data[ns:], nil)
}

// Token returns n random bytes hex-encoded.
func Token(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
