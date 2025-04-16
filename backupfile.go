package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	metadataFilename = "metadata.json"
	dataFilename     = "data.json"
)

type Metadata struct {
	NextToken string `json:"next_token"` // Token to use for the 'from' parameter in the next /messages request
}

// readMetadata loads the metadata file for a room.
func readMetadata(roomPath string) (*Metadata, error) {
	metaPath := filepath.Join(roomPath, metadataFilename)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Metadata{}, nil // Return empty metadata if file doesn't exist
		}
		return nil, fmt.Errorf("failed to read metadata file %s: %w", metaPath, err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata file %s: %w", metaPath, err)
	}
	return &meta, nil
}

// writeMetadata saves the metadata file for a room.
func writeMetadata(roomPath string, meta *Metadata) error {
	metaPath := filepath.Join(roomPath, metadataFilename)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata file %s: %w", metaPath, err)
	}
	return nil
}

// processEvents groups events by date and writes them to daily files.
// As multiple requests can span same day, results are merged.
func processEvents(roomPath string, events []*event.Event) error {
	eventsByDate := make(map[string][]*event.Event)
	for _, evt := range events {
		// Group by UTC date
		dateStr := time.UnixMilli(evt.Timestamp).UTC().Format("2006-01-02")
		eventsByDate[dateStr] = append(eventsByDate[dateStr], evt)
	}

	for dateStr, dailyEvents := range eventsByDate {
		datePath := filepath.Join(roomPath, dateStr)
		if err := os.MkdirAll(datePath, 0o755); err != nil {
			return fmt.Errorf("failed to create date directory %s: %w", datePath, err)
		}

		dataPath := filepath.Join(datePath, dataFilename)

		// Read existing data if file exists
		var existingEvents []*event.Event
		existingData, err := os.ReadFile(dataPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read existing data file %s: %w", dataPath, err)
		}
		if err == nil {
			// File exists, try to unmarshal
			if err := json.Unmarshal(existingData, &existingEvents); err != nil {
				// Log warning but proceed, potentially overwriting corrupted file
				log.Warn().Str("path", dataPath).Err(err).Msg("Failed to unmarshal existing data file, will overwrite")
				existingEvents = nil // Reset slice to ensure overwrite
			}
		}

		// Merge new events with existing ones, ensuring uniqueness by EventID
		mergedEventsMap := make(map[id.EventID]*event.Event)
		for _, evt := range existingEvents {
			mergedEventsMap[evt.ID] = evt
		}
		for _, evt := range dailyEvents {
			mergedEventsMap[evt.ID] = evt
		}

		// Convert map back to slice
		finalEvents := make([]*event.Event, 0, len(mergedEventsMap))
		for _, evt := range mergedEventsMap {
			finalEvents = append(finalEvents, evt)
		}

		// Sort events by timestamp for consistency
		sort.SliceStable(finalEvents, func(i, j int) bool {
			return finalEvents[i].Timestamp < finalEvents[j].Timestamp
		})

		// Marshal and write the merged and sorted data
		mergedData, err := json.MarshalIndent(finalEvents, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal merged events for date %s: %w", dateStr, err)
		}
		if err := os.WriteFile(dataPath, mergedData, 0o644); err != nil {
			return fmt.Errorf("failed to write merged data file %s: %w", dataPath, err)
		}
	}
	return nil
}

// updateMetadataToken saves the new token to the metadata file if it has changed.
func updateMetadataToken(roomPath string, meta *Metadata, newToken string, roomLog zerolog.Logger) {
	if newToken != meta.NextToken {
		meta.NextToken = newToken
		if err := writeMetadata(roomPath, meta); err != nil {
			roomLog.Error().Err(err).Msg("Failed to write updated metadata")
			// Don't return error here, as backup might have partially succeeded
		} else {
			roomLog.Debug().Str("token", meta.NextToken).Msg("Updated next sync token")
		}
	}
}
