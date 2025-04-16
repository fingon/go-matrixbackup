package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gotest.tools/v3/assert"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Helper to create a dummy event for testing
func newTestEvent(idStr string, ts int64, content string) *event.Event {
	return &event.Event{
		ID:        id.EventID(idStr),
		Timestamp: ts,
		Type:      event.EventMessage,
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: "m.text",
				Body:    content,
			},
		},
	}
}

func TestReadWriteMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	roomPath := filepath.Join(tmpDir, "testRoom")
	err := os.Mkdir(roomPath, 0o755)
	assert.NilError(t, err)

	metaPath := filepath.Join(roomPath, metadataFilename)

	t.Run("Write and Read", func(t *testing.T) {
		metaToWrite := &Metadata{NextToken: "token123"}
		err := writeMetadata(roomPath, metaToWrite)
		assert.NilError(t, err)

		// Check file content directly
		data, err := os.ReadFile(metaPath)
		assert.NilError(t, err)
		expectedJSON := `{
  "next_token": "token123"
}`
		assert.Equal(t, string(data), expectedJSON)

		// Read back using readMetadata
		metaRead, err := readMetadata(roomPath)
		assert.NilError(t, err)
		assert.DeepEqual(t, metaRead, metaToWrite)
	})

	t.Run("Read non-existent", func(t *testing.T) {
		// Ensure file doesn't exist first
		_ = os.Remove(metaPath)

		metaRead, err := readMetadata(roomPath)
		assert.NilError(t, err)
		// Should return empty metadata, not nil
		assert.Assert(t, metaRead != nil)
		assert.DeepEqual(t, metaRead, &Metadata{})
	})

	t.Run("Read invalid JSON", func(t *testing.T) {
		err := os.WriteFile(metaPath, []byte("{invalid json"), 0o644)
		assert.NilError(t, err)

		metaRead, err := readMetadata(roomPath)
		assert.ErrorContains(t, err, "failed to unmarshal metadata file")
		assert.Assert(t, metaRead == nil)
	})
}

func TestProcessEvents(t *testing.T) {
	tmpDir := t.TempDir()
	roomPath := filepath.Join(tmpDir, "testRoom")
	// No need to create roomPath beforehand, processEvents should create subdirs

	// Timestamps for specific dates (UTC)
	ts1 := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC).UnixMilli() // 2024-01-15
	ts2 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC).UnixMilli() // 2024-01-15
	ts3 := time.Date(2024, 1, 16, 9, 0, 0, 0, time.UTC).UnixMilli()  // 2024-01-16

	events1 := []*event.Event{
		newTestEvent("$evt1", ts1, "Hello"),
		newTestEvent("$evt3", ts3, "Goodbye"),
	}
	events2 := []*event.Event{
		newTestEvent("$evt2", ts2, "World"), // Same day as evt1
		newTestEvent("$evt1", ts1, "Hello"), // Duplicate event
	}

	datePath1 := filepath.Join(roomPath, "2024-01-15")
	dataPath1 := filepath.Join(datePath1, dataFilename)
	datePath2 := filepath.Join(roomPath, "2024-01-16")
	dataPath2 := filepath.Join(datePath2, dataFilename)

	t.Run("First batch", func(t *testing.T) {
		err := processEvents(roomPath, events1)
		assert.NilError(t, err)

		// Check 2024-01-15
		assert.Assert(t, fileExists(dataPath1))
		data, err := os.ReadFile(dataPath1)
		assert.NilError(t, err)
		var readEvents []*event.Event
		err = json.Unmarshal(data, &readEvents)
		assert.NilError(t, err)
		assert.Equal(t, len(readEvents), 1)
		assert.Equal(t, readEvents[0].ID, id.EventID("$evt1"))

		// Check 2024-01-16
		assert.Assert(t, fileExists(dataPath2))
		data, err = os.ReadFile(dataPath2)
		assert.NilError(t, err)
		err = json.Unmarshal(data, &readEvents)
		assert.NilError(t, err)
		assert.Equal(t, len(readEvents), 1)
		assert.Equal(t, readEvents[0].ID, id.EventID("$evt3"))
	})

	t.Run("Second batch - merge and sort", func(t *testing.T) {
		err := processEvents(roomPath, events2)
		assert.NilError(t, err)

		// Check 2024-01-15 (should now have evt1 and evt2, sorted)
		assert.Assert(t, fileExists(dataPath1))
		data, err := os.ReadFile(dataPath1)
		assert.NilError(t, err)
		var readEvents []*event.Event
		err = json.Unmarshal(data, &readEvents)
		assert.NilError(t, err)
		assert.Equal(t, len(readEvents), 2, "Should contain 2 unique events")
		assert.Equal(t, readEvents[0].ID, id.EventID("$evt1"), "Should be sorted by timestamp")
		assert.Equal(t, readEvents[1].ID, id.EventID("$evt2"), "Should be sorted by timestamp")

		// Check 2024-01-16 (should be unchanged)
		assert.Assert(t, fileExists(dataPath2))
		data, err = os.ReadFile(dataPath2)
		assert.NilError(t, err)
		err = json.Unmarshal(data, &readEvents)
		assert.NilError(t, err)
		assert.Equal(t, len(readEvents), 1)
		assert.Equal(t, readEvents[0].ID, id.EventID("$evt3"))
	})

	t.Run("Process empty events", func(t *testing.T) {
		// Reset by removing old files
		_ = os.RemoveAll(roomPath)
		err := processEvents(roomPath, []*event.Event{})
		assert.NilError(t, err)
		// Ensure no directories were created
		_, err = os.Stat(roomPath)
		assert.Assert(t, os.IsNotExist(err))
	})

	t.Run("Handle corrupted existing data file", func(t *testing.T) {
		// Reset
		_ = os.RemoveAll(roomPath)
		// Create a corrupted file for 2024-01-15
		err := os.MkdirAll(datePath1, 0o755)
		assert.NilError(t, err)
		err = os.WriteFile(dataPath1, []byte("[{invalid json"), 0o644)
		assert.NilError(t, err)

		// Process new events for the same day
		newEvents := []*event.Event{newTestEvent("$evt4", ts1+1, "New Data")}
		err = processEvents(roomPath, newEvents)
		assert.NilError(t, err) // Should log warning but not fail

		// Check if the file was overwritten correctly
		data, err := os.ReadFile(dataPath1)
		assert.NilError(t, err)
		var readEvents []*event.Event
		err = json.Unmarshal(data, &readEvents)
		assert.NilError(t, err)
		assert.Equal(t, len(readEvents), 1)
		assert.Equal(t, readEvents[0].ID, id.EventID("$evt4"))
	})
}

func TestUpdateMetadataToken(t *testing.T) {
	tmpDir := t.TempDir()
	roomPath := filepath.Join(tmpDir, "testRoom")
	err := os.Mkdir(roomPath, 0o755)
	assert.NilError(t, err)
	logger := zerolog.Nop()
	metaPath := filepath.Join(roomPath, metadataFilename)

	t.Run("Update needed", func(t *testing.T) {
		meta := &Metadata{NextToken: "old_token"}
		// Pre-write the initial metadata
		err := writeMetadata(roomPath, meta)
		assert.NilError(t, err)

		newToken := "new_token"
		updateMetadataToken(roomPath, meta, newToken, logger)

		// Check internal state
		assert.Equal(t, meta.NextToken, newToken)

		// Check file content
		readMeta, err := readMetadata(roomPath)
		assert.NilError(t, err)
		assert.Equal(t, readMeta.NextToken, newToken)
	})

	t.Run("No update needed", func(t *testing.T) {
		currentToken := "current_token"
		meta := &Metadata{NextToken: currentToken}
		// Pre-write the initial metadata
		err := writeMetadata(roomPath, meta)
		assert.NilError(t, err)

		// Get initial file mod time
		info, err := os.Stat(metaPath)
		assert.NilError(t, err)
		modTimeBefore := info.ModTime()

		// Sleep briefly to ensure mod time can change if file is written
		time.Sleep(2 * time.Millisecond)

		updateMetadataToken(roomPath, meta, currentToken, logger) // Same token

		// Check internal state
		assert.Equal(t, meta.NextToken, currentToken)

		// Check file content (should be unchanged)
		readMeta, err := readMetadata(roomPath)
		assert.NilError(t, err)
		assert.Equal(t, readMeta.NextToken, currentToken)

		// Check mod time (should be unchanged)
		info, err = os.Stat(metaPath)
		assert.NilError(t, err)
		modTimeAfter := info.ModTime()
		assert.Equal(t, modTimeAfter, modTimeBefore)
	})

	t.Run("Write fails", func(t *testing.T) {
		meta := &Metadata{NextToken: "token_before_fail"}
		newToken := "token_fail"

		// Make directory read-only to cause write failure
		err := os.Chmod(roomPath, 0o555)
		assert.NilError(t, err)
		defer func() {
			// Restore permissions for cleanup
			_ = os.Chmod(roomPath, 0o755)
		}()

		// updateMetadataToken should log the error but not return it
		updateMetadataToken(roomPath, meta, newToken, logger)

		// Internal state should still be updated
		assert.Equal(t, meta.NextToken, newToken)

		// File content should NOT be updated (verify by trying to read)
		// Need to make readable again first
		_ = os.Chmod(roomPath, 0o755)
		// Write the original state so readMetadata doesn't fail if the file wasn't created
		_ = os.WriteFile(metaPath, []byte(`{"next_token": "token_before_fail"}`), 0o644)

		readMeta, err := readMetadata(roomPath)
		assert.NilError(t, err)                                  // Read should succeed now
		assert.Equal(t, readMeta.NextToken, "token_before_fail") // Should contain the old token
	})
}

// Helper function to check if a file exists
func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}
