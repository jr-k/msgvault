package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.kenn.io/msgvault/internal/fileutil"
)

const installIDFilename = "telemetry-install-id"

type installRecord struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// loadOrCreateInstall returns the anonymous install ID in dataDir and when it was created.
func loadOrCreateInstall(dataDir string) (string, time.Time, error) {
	path := filepath.Join(dataDir, installIDFilename)
	data, err := os.ReadFile(path)
	if err == nil {
		return parseInstall(path, data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", time.Time{}, fmt.Errorf("read telemetry install ID %q: %w", path, err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generate telemetry install ID: %w", err)
	}
	record := installRecord{ID: hex.EncodeToString(raw[:]), CreatedAt: time.Now().UTC()}
	data, err = json.Marshal(record)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode telemetry install ID: %w", err)
	}
	if err := fileutil.SecureReplaceFile(path, data, 0o600); err != nil {
		return "", time.Time{}, fmt.Errorf("write telemetry install ID %q: %w", path, err)
	}
	return record.ID, record.CreatedAt, nil
}

func parseInstall(path string, data []byte) (string, time.Time, error) {
	var record installRecord
	invalid := fmt.Errorf("invalid telemetry install ID in %q: remove the file to create a new one", path)
	if err := json.Unmarshal(data, &record); err != nil {
		return "", time.Time{}, invalid
	}
	if raw, err := hex.DecodeString(record.ID); err != nil || len(raw) != 16 || record.CreatedAt.IsZero() {
		return "", time.Time{}, invalid
	}
	return record.ID, record.CreatedAt, nil
}
