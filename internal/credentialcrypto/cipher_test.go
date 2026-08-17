package credentialcrypto

import (
	"encoding/base64"
	"testing"
)

func TestCipherRequiresMatchingAssociatedData(t *testing.T) {
	cipher, err := New("session-secret")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt("activation-code", []byte("activation-code:id-1"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(ciphertext, []byte("activation-code:id-1"))
	if err != nil || plaintext != "activation-code" {
		t.Fatalf("凭据解密失败: plaintext=%q err=%v", plaintext, err)
	}
	if _, err := cipher.Decrypt(ciphertext, []byte("activation-code:id-2")); err == nil {
		t.Fatal("关联数据不匹配时必须拒绝解密")
	}
}

func TestCipherRejectsTampering(t *testing.T) {
	cipher, err := New("session-secret")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt("activation-code", []byte("activation-code:id-1"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := base64.RawStdEncoding.EncodeToString(raw)
	if _, err := cipher.Decrypt(tampered, []byte("activation-code:id-1")); err == nil {
		t.Fatal("被篡改的密文必须拒绝解密")
	}
}
