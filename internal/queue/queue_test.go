// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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

func TestNewConsumerValidations(t *testing.T) {
	tests := []struct {
		name    string
		dbURL   string
		wantErr bool
	}{
		{"Insecure sslmode=disable", "postgres://user:pass@localhost/db?sslmode=disable", true},
		{"Missing sslmode", "postgres://user:pass@localhost/db", true},
		{"Secure sslmode=require", "postgres://user:pass@localhost/db?sslmode=require", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewConsumer(tt.dbURL, "node", nil, nil)
			if tt.wantErr {
				if err == nil {
					t.Errorf("NewConsumer() expected error but got nil")
				}
			} else {
				if err != nil && (strings.Contains(err.Error(), "sslmode=disable") || strings.Contains(err.Error(), "sslmode must be explicitly set")) {
					t.Errorf("NewConsumer() unexpected security validation error: %v", err)
				}
			}
		})
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
	}
	for _, tt := range tests {
		if got := backoff(tt.attempt); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestVerifyTask(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	c := &Consumer{publicKey: pub}

	payload := []byte(`{"task_id":"123"}`)
	signedAt := time.Now().Unix()
	msg := append(payload, []byte(fmt.Sprintf("%d", signedAt))...)
	sig := ed25519.Sign(priv, msg)

	cmd := &claimedCommand{
		id:         "cmd1",
		rawPayload: payload,
		signature:  hex.EncodeToString(sig),
		signedAt:   signedAt,
	}

	if err := c.verifyTask(cmd); err != nil {
		t.Errorf("verifyTask failed: %v", err)
	}

	// Expired task
	cmd.signedAt = time.Now().Add(-10 * time.Minute).Unix()
	if err := c.verifyTask(cmd); err == nil {
		t.Error("expected error for expired task")
	}

	// Invalid signature
	cmd.signedAt = time.Now().Unix()
	cmd.signature = hex.EncodeToString([]byte("invalid"))
	if err := c.verifyTask(cmd); err == nil {
		t.Error("expected error for invalid signature")
	}
}

func TestDatabaseMethods(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	c := &Consumer{
		db:         db,
		nodeID:     "node1",
		consumerID: "cons1",
	}

	// markRunning
	mock.ExpectExec("UPDATE fleet_commands SET status = \\$1, started_at = CURRENT_TIMESTAMP WHERE id = \\$2").
		WithArgs("running", "cmd1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.markRunning("cmd1"); err != nil {
		t.Errorf("markRunning failed: %v", err)
	}

	// markSucceeded
	mock.ExpectExec("UPDATE fleet_commands SET status = \\$1, completed_at = CURRENT_TIMESTAMP, leased_until = NULL WHERE id = \\$2").
		WithArgs("succeeded", "cmd1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.markSucceeded("cmd1"); err != nil {
		t.Errorf("markSucceeded failed: %v", err)
	}

	// discardCommand
	mock.ExpectExec("UPDATE fleet_commands SET status = \\$1, last_error = \\$2, completed_at = CURRENT_TIMESTAMP, leased_until = NULL WHERE id = \\$3").
		WithArgs("failed", "signature: invalid", "cmd1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.discardCommand("cmd1", "invalid"); err != nil {
		t.Errorf("discardCommand failed: %v", err)
	}

	// renewLease
	c.leaseDuration = 5 * time.Minute
	mock.ExpectExec("UPDATE fleet_commands SET leased_until = CURRENT_TIMESTAMP \\+ \\(\\$1 \\* INTERVAL '1 second'\\) WHERE id = \\$2 AND leased_by = \\$3 AND status = \\$4").
		WithArgs(300, "cmd1", "cons1", "running").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.renewLease("cmd1"); err != nil {
		t.Errorf("renewLease failed: %v", err)
	}

	// markFailed - retry
	mock.ExpectExec("UPDATE fleet_commands SET status = \\$1, last_error = \\$2, available_at = \\$3, leased_until = NULL, leased_by = NULL WHERE id = \\$4").
		WithArgs("pending", "some error", sqlmock.AnyArg(), "cmd1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.markFailed(&claimedCommand{id: "cmd1", attemptCount: 1, maxAttempts: 3}, fmt.Errorf("some error")); err != nil {
		t.Errorf("markFailed retry failed: %v", err)
	}

	// markFailed - dead letter
	mock.ExpectExec("UPDATE fleet_commands SET status = \\$1, last_error = \\$2, completed_at = CURRENT_TIMESTAMP, leased_until = NULL WHERE id = \\$3").
		WithArgs("dead_letter", "fatal error", "cmd1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := c.markFailed(&claimedCommand{id: "cmd1", attemptCount: 3, maxAttempts: 3}, fmt.Errorf("fatal error")); err != nil {
		t.Errorf("markFailed dead letter failed: %v", err)
	}
}

func TestClaimNext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	c := &Consumer{
		db:            db,
		nodeID:        "node1",
		consumerID:    "cons1",
		leaseDuration: 5 * time.Minute,
	}

	// No rows case
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE fleet_commands").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE fleet_commands").WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	_, err = c.claimNext(context.Background())
	if err != errNoCommandAvailable {
		t.Errorf("expected errNoCommandAvailable, got %v", err)
	}

	// Success case
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE fleet_commands").WillReturnResult(sqlmock.NewResult(0, 1))
	rows := sqlmock.NewRows([]string{"id", "payload", "attempt_count", "max_attempts", "task_signature", "signed_at"}).
		AddRow("cmd1", []byte(`{"task_id":"123"}`), 1, 3, "sig", 123456789)
	mock.ExpectQuery("UPDATE fleet_commands").WillReturnRows(rows)
	mock.ExpectCommit()

	cmd, err := c.claimNext(context.Background())
	if err != nil {
		t.Errorf("claimNext failed: %v", err)
	}
	if cmd.id != "cmd1" {
		t.Errorf("expected cmd1, got %s", cmd.id)
	}
}

func TestOpenPayload(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c := &Consumer{encryptionKey: key}

	plaintext := []byte(`{"task_id":"secret"}`)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	b64Ct := base64.StdEncoding.EncodeToString(ciphertext)

	got, err := c.openPayload(b64Ct)
	if err != nil {
		t.Fatalf("openPayload failed: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("expected %s, got %s", plaintext, got)
	}

	// No key
	c.encryptionKey = nil
	_, err = c.openPayload(b64Ct)
	if err == nil {
		t.Error("expected error for no encryption key")
	}
}

func TestClose(t *testing.T) {
	db, mock, _ := sqlmock.New()
	c := &Consumer{db: db}
	mock.ExpectClose()
	c.Close()
}
