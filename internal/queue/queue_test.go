// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestLeaseRefreshInterval(t *testing.T) {
	tests := []struct {
		name  string
		lease time.Duration
		want  time.Duration
	}{
		{name: "short leases keep minimum cadence", lease: 30 * time.Second, want: 10 * time.Second},
		{name: "very short leases clamp to five seconds", lease: 9 * time.Second, want: 5 * time.Second},
		{name: "long leases renew at one third", lease: 6 * time.Minute, want: 2 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := leaseRefreshInterval(tt.lease)
			if got != tt.want {
				t.Fatalf("leaseRefreshInterval(%v) = %v, want %v", tt.lease, got, tt.want)
			}
		})
	}
}

func TestVerifyTaskNoKey(t *testing.T) {
	c := &Consumer{}
	cmd := &claimedCommand{id: "t1"}
	if err := c.verifyTask(cmd); err != nil {
		t.Fatalf("verifyTask should return nil if no public key: %v", err)
	}
}

func TestVerifyTask(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	c := &Consumer{publicKey: pub}

	payload := []byte("test-payload")
	signedAt := time.Now().Unix()
	msg := append(payload, []byte(fmt.Sprintf("%d", signedAt))...)
	sig := ed25519.Sign(priv, msg)

	tests := []struct {
		name    string
		cmd     *claimedCommand
		wantErr bool
	}{
		{
			name: "valid signature",
			cmd: &claimedCommand{
				id:         "t1",
				signature:  hex.EncodeToString(sig),
				signedAt:   signedAt,
				rawPayload: payload,
			},
			wantErr: false,
		},
		{
			name: "invalid signature",
			cmd: &claimedCommand{
				id:         "t1",
				signature:  hex.EncodeToString(make([]byte, 64)),
				signedAt:   signedAt,
				rawPayload: payload,
			},
			wantErr: true,
		},
		{
			name: "expired task",
			cmd: &claimedCommand{
				id:         "t1",
				signature:  hex.EncodeToString(sig),
				signedAt:   time.Now().Add(-10 * time.Minute).Unix(),
				rawPayload: payload,
			},
			wantErr: true,
		},
		{
			name: "missing signature",
			cmd: &claimedCommand{
				id: "t1",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.verifyTask(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("verifyTask() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOpenPayloadNoKey(t *testing.T) {
	c := &Consumer{}
	_, err := c.openPayload("anything")
	if err == nil {
		t.Fatal("expected error when no encryption key")
	}
}

func TestOpenPayload(t *testing.T) {
	key := make([]byte, 32)
	io.ReadFull(rand.Reader, key)
	c := &Consumer{encryptionKey: key}

	plaintext := []byte("secret message")
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	io.ReadFull(rand.Reader, nonce)
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	b64 := base64.StdEncoding.EncodeToString(ciphertext)

	tests := []struct {
		name    string
		input   string
		want    []byte
		wantErr bool
	}{
		{"valid", b64, plaintext, false},
		{"invalid base64", "!!!", nil, true},
		{"too short", base64.StdEncoding.EncodeToString(make([]byte, 1)), nil, true},
		{"wrong key", b64, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldKey := c.encryptionKey
			if tt.name == "wrong key" {
				c.encryptionKey = make([]byte, 32)
			}
			got, err := c.openPayload(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("openPayload() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && string(got) != string(tt.want) {
				t.Errorf("openPayload() = %q, want %q", got, tt.want)
			}
			c.encryptionKey = oldKey
		})
	}
}

func TestNewConsumerValidation(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"postgres://user:pass@localhost/db?sslmode=require", false},
		{"postgres://user:pass@localhost/db?sslmode=disable", true},
		{"postgres://user:pass@localhost/db", true},
	}

	for _, tt := range tests {
		_, err := NewConsumer(tt.url, "node1", nil, nil)
		if (err != nil) != tt.wantErr {
			t.Errorf("NewConsumer(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
		}
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 10 * time.Second},
		{4, 30 * time.Second},
		{10, 30 * time.Second},
	}
	for _, tt := range tests {
		got := backoff(tt.attempt)
		if got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}
