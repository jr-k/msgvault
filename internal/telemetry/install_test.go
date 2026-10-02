package telemetry

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateInstallCreatesThenReuses(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()

	id, created, err := loadOrCreateInstall(dir)
	require.NoError(err)
	raw, err := hex.DecodeString(id)
	require.NoError(err)
	assert.Len(raw, 16)
	assert.False(created.IsZero())
	path := filepath.Join(dir, installIDFilename)
	info, err := os.Stat(path)
	require.NoError(err)
	if runtime.GOOS != "windows" {
		assert.Equal(os.FileMode(0o600), info.Mode().Perm())
	}

	againID, againCreated, err := loadOrCreateInstall(dir)
	require.NoError(err)
	assert.Equal(id, againID)
	assert.True(created.Equal(againCreated))
}

func TestLoadOrCreateInstallRejectsCorruptFile(t *testing.T) {
	cases := map[string]string{
		"not json":           `not json`,
		"short ID":           `{"id":"abcd","created_at":"2026-01-01T00:00:00Z"}`,
		"missing created_at": `{"id":"abababababababababababababababab"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			dir := t.TempDir()
			path := filepath.Join(dir, installIDFilename)
			require.NoError(os.WriteFile(path, []byte(content), 0o600))

			_, _, err := loadOrCreateInstall(dir)
			require.Error(err)
			assert.Contains(err.Error(), strconv.Quote(path))
			after, readErr := os.ReadFile(path)
			require.NoError(readErr)
			assert.Equal(content, string(after))
		})
	}
}

func TestLoadOrCreateInstallFailsWhenDataDirIsAFile(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))

	_, _, err := loadOrCreateInstall(notDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), notDir)
}
