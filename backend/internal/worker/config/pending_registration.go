package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// pendingRecordName is the durable registration attempt record: the
// identity, the Hub endpoint, the key, and the complete private keypair
// a registration pinned. It exists BEFORE the first network call and
// survives response loss, cancellation, timeout, and process restart.
const pendingRecordName = "pending-registration.json"

// PendingRegistration is the durable attempt record. Its keypair is the
// one the Hub pins; recovery proves possession of its private halves.
type PendingRegistration struct {
	AttemptID string `json:"attempt_id"`
	HubURL    string `json:"hub_url"`
	RegKey    string `json:"registration_key"`
	KeyMaterial
}

// PendingPath returns the pending record's path under the data dir.
func (c *Config) PendingPath() string {
	return filepath.Join(c.DataDir, pendingRecordName)
}

// LoadPendingRegistration reads the durable attempt record. A missing
// record is (nil, nil): nothing is in flight.
func (c *Config) LoadPendingRegistration() (*PendingRegistration, error) {
	data, err := os.ReadFile(c.PendingPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var pending PendingRegistration
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("parse the pending registration %s: %w", c.PendingPath(), err)
	}
	if pending.AttemptID == "" || pending.HubURL == "" || pending.RegKey == "" || !pending.HasKeypair() {
		return nil, fmt.Errorf("the pending registration %s misses a required field; delete it and register again", c.PendingPath())
	}
	return &pending, nil
}

// SavePendingRegistration durably publishes the attempt record. Durable
// publication BEFORE any network request is the whole protocol: a
// crash after the request but before this write leaves a consumed key
// with no recoverable attempt.
func (c *Config) SavePendingRegistration(p *PendingRegistration) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(c.PendingPath(), data, 0o600)
}

// RemovePendingRegistration removes the attempt record and every
// interrupted copy beside it. It succeeds for an absent record and
// joins independent failures, so a repeated register finishes a partial
// cleanup.
func (c *Config) RemovePendingRegistration() error {
	return RemoveSensitiveRecord(c.PendingPath())
}
