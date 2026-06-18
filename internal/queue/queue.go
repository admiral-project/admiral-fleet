package queue

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
	_ "github.com/lib/pq" // register PostgreSQL driver
)

const (
	defaultPollInterval  = 2 * time.Second
	defaultLeaseDuration = 5 * time.Minute
	taskMaxAge           = 5 * time.Minute
)

var errNoCommandAvailable = errors.New("no command available")

type Consumer struct {
	db            *sql.DB
	nodeID        string
	consumerID    string
	pollInterval  time.Duration
	leaseDuration time.Duration
	publicKey     ed25519.PublicKey
	encryptionKey []byte
}

type claimedCommand struct {
	id           string
	task         admiral.FleetTask
	attemptCount int
	maxAttempts  int
	signature    string
	signedAt     int64
	rawPayload   []byte
}

func NewConsumer(dbURL, nodeID string, publicKey ed25519.PublicKey, encryptionKey []byte) (*Consumer, error) {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("open queue database: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping queue database: %w", err)
	}
	return &Consumer{
		db:            db,
		nodeID:        nodeID,
		consumerID:    fmt.Sprintf("%s-%d", nodeID, time.Now().UnixNano()),
		pollInterval:  defaultPollInterval,
		leaseDuration: defaultLeaseDuration,
		publicKey:     publicKey,
		encryptionKey: encryptionKey,
	}, nil
}

func (c *Consumer) ConsumeLoop(handler func(admiral.FleetTask) error) {
	for {
		cmd, err := c.claimNext(context.Background())
		if err != nil {
			if errors.Is(err, errNoCommandAvailable) {
				time.Sleep(c.pollInterval)
				continue
			}
			slog.Error("queue claim failed", "error", err)
			time.Sleep(c.pollInterval)
			continue
		}

		if err := c.verifyTask(cmd); err != nil {
			slog.Error("task verification failed, discarding", "command_id", cmd.id, "error", err)
			if derr := c.discardCommand(cmd.id, err.Error()); derr != nil {
				slog.Error("failed to discard invalid command", "command_id", cmd.id, "error", derr)
			}
			continue
		}

		if err := c.markRunning(cmd.id); err != nil {
			slog.Error("failed to mark command running", "command_id", cmd.id, "error", err)
			time.Sleep(c.pollInterval)
			continue
		}
		stopRenew := c.startLeaseRenewer(cmd.id)

		if err := handler(cmd.task); err != nil {
			stopRenew()
			if ferr := c.markFailed(cmd, err); ferr != nil {
				slog.Error("failed to mark command failure", "command_id", cmd.id, "error", ferr)
			}
			continue
		}

		stopRenew()
		if err := c.markSucceeded(cmd.id); err != nil {
			slog.Error("failed to mark command success", "command_id", cmd.id, "error", err)
		}
	}
}

func (c *Consumer) verifyTask(cmd *claimedCommand) error {
	if c.publicKey == nil {
		return nil
	}
	if cmd.signature == "" || cmd.signedAt == 0 {
		return fmt.Errorf("task %s has no signature", cmd.id)
	}
	age := time.Since(time.Unix(cmd.signedAt, 0))
	if age > taskMaxAge {
		return fmt.Errorf("task %s expired: signed %v ago (max %v)", cmd.id, age, taskMaxAge)
	}
	sig, err := hex.DecodeString(cmd.signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("task %s has invalid signature encoding", cmd.id)
	}
	msg := append(cmd.rawPayload, []byte(fmt.Sprintf("%d", cmd.signedAt))...)
	if !ed25519.Verify(c.publicKey, msg, sig) {
		return fmt.Errorf("task %s signature verification failed", cmd.id)
	}
	return nil
}

func (c *Consumer) openPayload(b64Ciphertext string) ([]byte, error) {
	if len(c.encryptionKey) == 0 {
		return nil, fmt.Errorf("task is encrypted but no encryption key configured")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(b64Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode base64 ciphertext: %w", err)
	}
	block, err := aes.NewCipher(c.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt payload: %w", err)
	}
	return plaintext, nil
}

func (c *Consumer) discardCommand(id, reason string) error {
	_, err := c.db.Exec(`
		UPDATE fleet_commands
		SET status = $1,
			last_error = $2,
			completed_at = CURRENT_TIMESTAMP,
			leased_until = NULL
		WHERE id = $3
	`, string(admiral.CommandFailed), "signature: "+reason, id)
	return err
}

func leaseRefreshInterval(leaseDuration time.Duration) time.Duration {
	interval := leaseDuration / 3
	if interval < 5*time.Second {
		return 5 * time.Second
	}
	return interval
}

func (c *Consumer) startLeaseRenewer(id string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	ticker := time.NewTicker(leaseRefreshInterval(c.leaseDuration))

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.renewLease(id); err != nil {
					slog.Error("failed to renew command lease", "command_id", id, "error", err)
				}
			}
		}
	}()

	return cancel
}

func (c *Consumer) claimNext(ctx context.Context) (*claimedCommand, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE fleet_commands
		SET status = $1,
			leased_until = NULL,
			leased_by = NULL
		WHERE node_id = $2
		  AND status IN ($3, $4)
		  AND leased_until IS NOT NULL
		  AND leased_until < CURRENT_TIMESTAMP
	`, string(admiral.CommandPending), c.nodeID, string(admiral.CommandLeased), string(admiral.CommandRunning)); err != nil {
		return nil, fmt.Errorf("reset expired leases: %w", err)
	}

	row := tx.QueryRowContext(ctx, `
		WITH next_command AS (
			SELECT id
			FROM fleet_commands
			WHERE node_id = $1
			  AND status = $2
			  AND available_at <= CURRENT_TIMESTAMP
			ORDER BY priority ASC, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE fleet_commands fc
		SET status = $3,
			leased_by = $4,
			leased_until = CURRENT_TIMESTAMP + ($5 * INTERVAL '1 second'),
			attempt_count = attempt_count + 1
		FROM next_command
		WHERE fc.id = next_command.id
		RETURNING fc.id, fc.payload, fc.attempt_count, fc.max_attempts, fc.task_signature, fc.signed_at
	`, c.nodeID, string(admiral.CommandPending), string(admiral.CommandLeased), c.consumerID, int(c.leaseDuration.Seconds()))

	var (
		id           string
		payload      []byte
		attemptCount int
		maxAttempts  int
		sig          sql.NullString
		signedAt     sql.NullInt64
	)
	if err := row.Scan(&id, &payload, &attemptCount, &maxAttempts, &sig, &signedAt); err != nil {
		if err == sql.ErrNoRows {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit empty claim tx: %w", err)
			}
			return nil, errNoCommandAvailable
		}
		return nil, fmt.Errorf("scan claimed command: %w", err)
	}

	var task admiral.FleetTask
	if err := json.Unmarshal(payload, &task); err != nil {
		// Check if payload is an encrypted wrapper
		var wrapper struct {
			Ct string `json:"ct"`
		}
		if uerr := json.Unmarshal(payload, &wrapper); uerr != nil || wrapper.Ct == "" {
			return nil, fmt.Errorf("decode command payload: %w", err)
		}
		plaintext, derr := c.openPayload(wrapper.Ct)
		if derr != nil {
			return nil, fmt.Errorf("decrypt command payload: %w", derr)
		}
		if uerr := json.Unmarshal(plaintext, &task); uerr != nil {
			return nil, fmt.Errorf("decode decrypted command payload: %w", uerr)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim tx: %w", err)
	}

	sigStr := ""
	if sig.Valid {
		sigStr = sig.String
	}
	signedAtVal := int64(0)
	if signedAt.Valid {
		signedAtVal = signedAt.Int64
	}

	return &claimedCommand{
		id:           id,
		task:         task,
		attemptCount: attemptCount,
		maxAttempts:  maxAttempts,
		signature:    sigStr,
		signedAt:     signedAtVal,
		rawPayload:   payload,
	}, nil
}

func (c *Consumer) markRunning(id string) error {
	_, err := c.db.Exec(`
		UPDATE fleet_commands
		SET status = $1,
			started_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`, string(admiral.CommandRunning), id)
	if err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	return nil
}

func (c *Consumer) markSucceeded(id string) error {
	_, err := c.db.Exec(`
		UPDATE fleet_commands
		SET status = $1,
			completed_at = CURRENT_TIMESTAMP,
			leased_until = NULL
		WHERE id = $2
	`, string(admiral.CommandSucceeded), id)
	if err != nil {
		return fmt.Errorf("mark succeeded: %w", err)
	}
	return nil
}

func (c *Consumer) renewLease(id string) error {
	_, err := c.db.Exec(`
		UPDATE fleet_commands
		SET leased_until = CURRENT_TIMESTAMP + ($1 * INTERVAL '1 second')
		WHERE id = $2
		  AND leased_by = $3
		  AND status = $4
	`, int(c.leaseDuration.Seconds()), id, c.consumerID, string(admiral.CommandRunning))
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	return nil
}

func (c *Consumer) markFailed(cmd *claimedCommand, taskErr error) error {
	nextStatus := admiral.CommandPending
	availableAt := time.Now().UTC().Add(backoff(cmd.attemptCount))
	if cmd.attemptCount >= cmd.maxAttempts {
		nextStatus = admiral.CommandDeadLetter
	}

	if nextStatus == admiral.CommandDeadLetter {
		_, err := c.db.Exec(`
			UPDATE fleet_commands
			SET status = $1,
				last_error = $2,
				completed_at = CURRENT_TIMESTAMP,
				leased_until = NULL
			WHERE id = $3
		`, string(nextStatus), taskErr.Error(), cmd.id)
		if err != nil {
			return fmt.Errorf("mark dead letter: %w", err)
		}
		return nil
	}

	_, err := c.db.Exec(`
		UPDATE fleet_commands
		SET status = $1,
			last_error = $2,
			available_at = $3,
			leased_until = NULL,
			leased_by = NULL
		WHERE id = $4
	`, string(nextStatus), taskErr.Error(), availableAt, cmd.id)
	if err != nil {
		return fmt.Errorf("mark retry pending: %w", err)
	}
	return nil
}

func backoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 2 * time.Second
	case attempt == 2:
		return 5 * time.Second
	case attempt == 3:
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}

func (c *Consumer) Close() {
	if c.db != nil {
		_ = c.db.Close()
	}
}
