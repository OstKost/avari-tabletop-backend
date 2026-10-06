package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileResetMailerPrivateDeliveryAndRetention(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "mailbox")
	mailer := &FileResetMailer{Directory: directory}
	require.NoError(t, mailer.SendReset(context.Background(), "synthetic@example.invalid", "synthetic-reset-token", time.Now().Add(15*time.Minute)))
	info, err := os.Stat(directory)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	file := filepath.Join(directory, entries[0].Name())
	info, err = os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	body, err := os.ReadFile(file)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(body, &record))
	require.Len(t, record, 3)
	old := time.Now().Add(-16 * time.Minute)
	require.NoError(t, os.Chtimes(file, old, old))
	require.NoError(t, mailer.SendReset(context.Background(), "other@example.invalid", "other-synthetic-token", time.Now().Add(15*time.Minute)))
	_, err = os.Stat(file)
	require.True(t, os.IsNotExist(err))
}
