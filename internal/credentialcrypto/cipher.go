package credentialcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

var ErrCiphertextInvalid = errors.New("凭据密文无效")

type Cipher struct {
	key [32]byte
}

func New(rootSecret string) (*Cipher, error) {
	secret := strings.TrimSpace(rootSecret)
	if secret == "" {
		return nil, errors.New("凭据加密根密钥不能为空")
	}
	return &Cipher{key: sha256.Sum256([]byte("deskpatrol.activation-code.v1\x00" + secret))}, nil
}

func (c *Cipher) Encrypt(plaintext string, associatedData []byte) (string, error) {
	if strings.TrimSpace(plaintext) == "" {
		return "", errors.New("不能加密空凭据")
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), associatedData)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(ciphertext string, associatedData []byte) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(ciphertext))
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrCiphertextInvalid
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], associatedData)
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	return string(plaintext), nil
}

func (c *Cipher) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
