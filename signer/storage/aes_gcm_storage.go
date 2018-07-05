//
// (at your option) any later version.
//
//
//

package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"

	"github.com/5uwifi/canchain/basis/log4j"
)

type storedCredential struct {
	// The iv
	Iv []byte `json:"iv"`
	// The ciphertext
	CipherText []byte `json:"c"`
}

type AESEncryptedStorage struct {
	// File to read/write credentials
	filename string
	// Key stored in base64
	key []byte
}

func NewAESEncryptedStorage(filename string, key []byte) *AESEncryptedStorage {
	return &AESEncryptedStorage{
		filename: filename,
		key:      key,
	}
}

func (s *AESEncryptedStorage) Put(key, value string) {
	if len(key) == 0 {
		return
	}
	data, err := s.readEncryptedStorage()
	if err != nil {
		log4j.Warn("Failed to read encrypted storage", "err", err, "file", s.filename)
		return
	}
	ciphertext, iv, err := encrypt(s.key, []byte(value))
	if err != nil {
		log4j.Warn("Failed to encrypt entry", "err", err)
		return
	}
	encrypted := storedCredential{Iv: iv, CipherText: ciphertext}
	data[key] = encrypted
	if err = s.writeEncryptedStorage(data); err != nil {
		log4j.Warn("Failed to write entry", "err", err)
	}
}

func (s *AESEncryptedStorage) Get(key string) string {
	if len(key) == 0 {
		return ""
	}
	data, err := s.readEncryptedStorage()
	if err != nil {
		log4j.Warn("Failed to read encrypted storage", "err", err, "file", s.filename)
		return ""
	}
	encrypted, exist := data[key]
	if !exist {
		log4j.Warn("Key does not exist", "key", key)
		return ""
	}
	entry, err := decrypt(s.key, encrypted.Iv, encrypted.CipherText)
	if err != nil {
		log4j.Warn("Failed to decrypt key", "key", key)
		return ""
	}
	return string(entry)
}

func (s *AESEncryptedStorage) readEncryptedStorage() (map[string]storedCredential, error) {
	creds := make(map[string]storedCredential)
	raw, err := ioutil.ReadFile(s.filename)

	if err != nil {
		if os.IsNotExist(err) {
			// Doesn't exist yet
			return creds, nil
		}
		log4j.Warn("Failed to read encrypted storage", "err", err, "file", s.filename)
	}
	if err = json.Unmarshal(raw, &creds); err != nil {
		log4j.Warn("Failed to unmarshal encrypted storage", "err", err, "file", s.filename)
		return nil, err
	}
	return creds, nil
}

func (s *AESEncryptedStorage) writeEncryptedStorage(creds map[string]storedCredential) error {
	raw, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if err = ioutil.WriteFile(s.filename, raw, 0600); err != nil {
		return err
	}
	return nil
}

func encrypt(key []byte, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func decrypt(key []byte, nonce []byte, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
