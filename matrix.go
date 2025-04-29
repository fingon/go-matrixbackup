package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	fetchLimit = 100 // Number of messages to fetch per request
)

// getRoomName tries to find a human-readable name for the room.
func getRoomName(ctx context.Context, logger zerolog.Logger, client *mautrix.Client, roomID id.RoomID) (string, error) {
	// 1. Try canonical alias
	var aliasResp event.CanonicalAliasEventContent
	err := client.StateEvent(ctx, roomID, event.StateCanonicalAlias, "", &aliasResp)
	if err == nil && aliasResp.Alias != "" {
		logger.Debug().Str("alias", string(aliasResp.Alias)).Msg("Using canonical alias")
		return string(aliasResp.Alias), nil
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		logger.Warn().Err(err).Msg("Failed to get canonical alias")
	}

	// 2. Try room name
	var nameResp event.RoomNameEventContent
	err = client.StateEvent(ctx, roomID, event.StateRoomName, "", &nameResp)
	if err == nil && nameResp.Name != "" {
		logger.Debug().Str("name", nameResp.Name).Msg("Using room name")
		return nameResp.Name, nil
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		logger.Warn().Err(err).Msg("Failed to get room name")
	}

	// 3. Fallback to Room ID
	logger.Debug().Str("room_id", roomID.String()).Msg("Using room ID as name")
	return string(roomID), nil
}

// fetchAndProcessRoomMessages contains the main loop for fetching messages and processing them.
func fetchAndProcessRoomMessages(ctx context.Context, client *mautrix.Client, roomID id.RoomID, roomPath, initialToken string, roomLog zerolog.Logger, cli *CLI) (string, int, error) {
	currentToken := initialToken
	fetchDirection := mautrix.DirectionForward
	totalFetched := 0
	for {
		roomLog.Debug().Str("direction", string(fetchDirection)).Str("token", currentToken).Int("limit", fetchLimit).Msg("Fetching messages")
		resp, err := client.Messages(ctx, roomID, currentToken, "", fetchDirection, nil, fetchLimit)
		if err != nil {
			roomLog.Error().Err(err).Msg("Failed to fetch messages")
			return currentToken, totalFetched, err
		}

		if len(resp.Chunk) == 0 {
			roomLog.Debug().Msg("Fetched empty chunk, sync complete")
			break
		}

		roomLog.Debug().Int("count", len(resp.Chunk)).Str("start_token", resp.Start).Str("end_token", resp.End).Msg("Fetched message chunk")

		if err := processEvents(roomPath, resp.Chunk); err != nil {
			roomLog.Error().Err(err).Msg("Failed to process message chunk")
			return currentToken, totalFetched, err
		}
		totalFetched += len(resp.Chunk)

		nextToken := resp.End

		if currentToken == nextToken {
			roomLog.Debug().Msg("Reached end of history (token did not change)")
			break
		}
		currentToken = nextToken

		// Small delay to avoid hammering the server
		time.Sleep(cli.FetchDelay)
	}
	return currentToken, totalFetched, nil
}

// backupRoom handles the backup logic for a single room.
func backupRoom(ctx context.Context, logger zerolog.Logger, client *mautrix.Client, roomID id.RoomID, cli *CLI) error {
	roomLog := logger.With().Str("room_id", roomID.String()).Logger()

	roomName, err := getRoomName(ctx, roomLog, client, roomID)
	if err != nil {
		roomLog.Error().Err(err).Msg("Failed to get room name, skipping room")
		return err // Skip room if we can't even get a name/ID
	}
	sanitizedName := sanitizeFilename(roomName)
	if sanitizedName != roomName {
		roomLog = roomLog.With().Str("room_name", roomName).Str("sanitized_name", sanitizedName).Logger()
	} else {
		roomLog = roomLog.With().Str("room_name", roomName).Logger()
	}

	// Construct directory name as sanitizedName:roomID
	roomDirName := sanitizedName + ":" + roomID.String()
	roomPath := filepath.Join(cli.BackupDir, roomDirName)
	roomLog = roomLog.With().Str("room_dir", roomDirName).Logger()

	// Ensure the target directory exists before potentially merging into it
	if err := os.MkdirAll(roomPath, 0o755); err != nil {
		roomLog.Error().Str("path", roomPath).Err(err).Msg("Failed to create room directory, skipping room")
		return err
	}

	// Merge data from any old directories for the same room ID
	if err := mergeOldRoomData(cli.BackupDir, roomID, roomDirName, roomPath, roomLog); err != nil {
		// Log the error but continue, as merging is best-effort
		roomLog.Warn().Err(err).Msg("Failed to merge data from old room directories")
	}

	meta, err := readMetadata(roomPath)
	if err != nil {
		// Assuming readMetadata doesn't log the error itself
		roomLog.Error().Str("path", roomPath).Err(err).Msg("Failed to read metadata, skipping room")
		return err
	}
	finalToken, totalFetched, err := fetchAndProcessRoomMessages(ctx, client, roomID, roomPath, meta.NextToken, roomLog, cli)
	if err != nil {
		// Error already logged within fetchAndProcessRoomMessages or handleInvalidToken
		return err // Propagate error to stop processing this room
	}

	// Update metadata with the latest token for the next run
	updateMetadataToken(roomPath, meta, finalToken, roomLog)

	if totalFetched > 0 {
		roomLog.Info().Int("total_fetched", totalFetched).Msg("Room backup finished")
	}
	return nil
}

// mergeOldRoomData finds directories in backupDir belonging to the same roomID but potentially
// different sanitized names, merges their event data into targetRoomPath, and removes the old directories.
func mergeOldRoomData(backupDir string, roomID id.RoomID, currentRoomDirName, targetRoomPath string, roomLog zerolog.Logger) error {
	roomIDStr := roomID.String()
	dirEntries, err := os.ReadDir(backupDir)
	if err != nil {
		// If we can't read the backup dir, we can't merge, but it might not exist yet.
		if os.IsNotExist(err) {
			return nil // Nothing to merge from
		}
		return fmt.Errorf("failed to read backup directory %s: %w", backupDir, err)
	}

	var mergeErrors []error
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()

		// Find the index of the ":!" separator which marks the start of the room ID.
		// This reliably separates the sanitized name from the actual room ID.
		separatorIndex := strings.LastIndex(dirName, ":!")

		// Check if the ":!" separator was found.
		if separatorIndex == -1 {
			continue
		}
		// Extract the potential room ID part (starts from the '!')
		extractedRoomID := dirName[separatorIndex+1:]

		// Check if the extracted ID matches the current room ID
		// AND that this isn't the directory we are currently processing.
		if extractedRoomID != roomIDStr || dirName == currentRoomDirName {
			continue
		}

		// This directory belongs to the same room but has a different name prefix. Merge it.
		err := processSingleOldDirectory(backupDir, dirName, targetRoomPath, roomLog)
		if err != nil {
			// Log the error from processing the single directory and add it to the list
			roomLog.Error().Err(err).Str("old_dir", dirName).Msg("Failed to process old directory")
			mergeErrors = append(mergeErrors, err) // Add the specific error from the function
		}
	}

	if len(mergeErrors) > 0 {
		// Combine multiple errors into one
		errorMessages := make([]string, len(mergeErrors))
		for i, e := range mergeErrors {
			errorMessages[i] = e.Error()
		}
		return errors.New("encountered errors during merge: " + strings.Join(errorMessages, "; "))
	}

	return nil
}

// processSingleOldDirectory reads events from a specific old directory, processes them into the target path,
// and removes the old directory. It returns an error if any step fails critically.
func processSingleOldDirectory(backupDir, oldDirName, targetRoomPath string, roomLog zerolog.Logger) error {
	oldDirPath := filepath.Join(backupDir, oldDirName)
	roomLog.Info().Str("old_dir", oldDirName).Msg("Found old directory for the same room, merging data")

	files, err := os.ReadDir(oldDirPath)
	if err != nil {
		// Log here, but return a wrapped error for the caller
		roomLog.Error().Err(err).Str("path", oldDirPath).Msg("Failed to read old directory")
		return fmt.Errorf("failed to read old dir %s: %w", oldDirName, err)
	}

	var allEvents []*event.Event
	var fileReadErrors []error
	for _, file := range files {
		// Skip subdirectories and the metadata file within the old directory
		if file.IsDir() || file.Name() == metadataFilename {
			continue
		}
		// Only process JSON files (assuming event data files end with .json)
		if !strings.HasSuffix(file.Name(), ".json") {
			roomLog.Debug().Str("file", file.Name()).Msg("Skipping non-JSON file in old directory")
			continue
		}

		filePath := filepath.Join(oldDirPath, file.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			roomLog.Error().Err(err).Str("path", filePath).Msg("Failed to read file from old directory, skipping file")
			fileReadErrors = append(fileReadErrors, fmt.Errorf("failed to read file %s in old dir %s: %w", file.Name(), oldDirName, err))
			continue // Skip this file, try others
		}

		var events []*event.Event
		if err := json.Unmarshal(data, &events); err != nil {
			roomLog.Error().Err(err).Str("path", filePath).Msg("Failed to unmarshal events from old file, skipping file")
			fileReadErrors = append(fileReadErrors, fmt.Errorf("failed to unmarshal %s in old dir %s: %w", file.Name(), oldDirName, err))
			continue // Skip this file, try others
		}
		allEvents = append(allEvents, events...)
	}

	// Log accumulated file read/unmarshal errors, but proceed if we have any events
	if len(fileReadErrors) > 0 {
		errorMessages := make([]string, len(fileReadErrors))
		for i, e := range fileReadErrors {
			errorMessages[i] = e.Error()
		}
		roomLog.Warn().Str("old_dir", oldDirName).Msg("Encountered errors reading files in old directory: " + strings.Join(errorMessages, "; "))
	}

	if len(allEvents) > 0 {
		roomLog.Debug().Int("count", len(allEvents)).Str("old_dir", oldDirName).Msg("Processing merged events from old directory")
		if err := processEvents(targetRoomPath, allEvents); err != nil {
			roomLog.Error().Err(err).Str("old_dir", oldDirName).Msg("Failed to process merged events from old directory")
			// Return this error, as failure to process means we shouldn't remove the old dir
			// Combine processing error with any previous file read errors for a comprehensive error message
			combinedError := fmt.Errorf("failed to process events from old dir %s: %w", oldDirName, err)
			if len(fileReadErrors) > 0 {
				errorMessages := make([]string, len(fileReadErrors))
				for i, e := range fileReadErrors {
					errorMessages[i] = e.Error()
				}
				combinedError = fmt.Errorf("%w; also encountered file read errors: %s", combinedError, strings.Join(errorMessages, "; "))
			}
			return combinedError
		}
	} else {
		roomLog.Debug().Str("old_dir", oldDirName).Msg("No valid event files found in old directory to merge")
	}

	// Only remove the old directory if processing succeeded (or there was nothing to process)
	roomLog.Info().Str("old_dir", oldDirName).Msg("Removing old directory after merging")
	if err := os.RemoveAll(oldDirPath); err != nil {
		roomLog.Error().Err(err).Str("path", oldDirPath).Msg("Failed to remove old directory after merging")
		// Return this error, but processing was successful
		return fmt.Errorf("failed to remove old dir %s after merging: %w", oldDirName, err)
	}

	// If we had file read errors but processing succeeded and removal succeeded, return nil
	// The warnings about file read errors have already been logged.
	return nil
}

// initializeMatrixClient creates and verifies the Matrix client connection.
func initializeMatrixClient(cli *CLI, logger zerolog.Logger) (*mautrix.Client, error) {
	logger.Info().Msg("Initializing Matrix client...")
	client, err := mautrix.NewClient(cli.Server, id.UserID(cli.User), cli.Token)
	if err != nil {
		// Log details before returning wrapped error
		logger.Error().Err(err).Msg("Failed to create Matrix client")
		return nil, fmt.Errorf("failed to create Matrix client: %w", err)
	}
	client.DeviceID = id.DeviceID(cli.DeviceID)
	client.Store = mautrix.NewMemorySyncStore() // We don't need sync store for backup

	whoami, err := client.Whoami(context.Background())
	if err != nil {
		// Log details before returning wrapped error
		logger.Error().Err(err).Msg("Failed to verify credentials (whoami failed)")
		// Attempt to provide more context if it's an HTTP error
		var httpErr mautrix.HTTPError
		if errors.As(err, &httpErr) {
			logger.Error().Int("status_code", httpErr.Response.StatusCode).Interface("resp_error", httpErr.RespError).Msg("Whoami HTTP error details")
		}
		return nil, fmt.Errorf("failed to verify credentials (whoami failed): %w", err)
	}
	logger.Info().Str("user_id", whoami.UserID.String()).Str("device_id", whoami.DeviceID.String()).Msg("Successfully logged in")
	if cli.DeviceID != "" && whoami.DeviceID != id.DeviceID(cli.DeviceID) {
		logger.Warn().Str("expected", cli.DeviceID).Str("actual", string(whoami.DeviceID)).Msg("Logged in with different device ID than specified")
	}
	client.DeviceID = whoami.DeviceID // Use actual device ID from whoami response
	return client, nil
}

// backupJoinedRooms fetches the list of joined rooms and initiates backup for each.
func backupJoinedRooms(ctx context.Context, client *mautrix.Client, cli *CLI, logger zerolog.Logger) error {
	logger.Info().Msg("Fetching list of joined rooms...")
	joinedRoomsResp, err := client.JoinedRooms(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to fetch joined rooms")
		return err // Return error to main
	}
	logger.Info().Int("count", len(joinedRoomsResp.JoinedRooms)).Msg("Found joined rooms")

	// Create base backup directory
	if err := os.MkdirAll(cli.BackupDir, 0o755); err != nil {
		logger.Error().Str("dir", cli.BackupDir).Err(err).Msg("Failed to create base backup directory")
		return err // Return error to main
	}

	// Backup each room
	var backupErrors []error
	for _, roomID := range joinedRoomsResp.JoinedRooms {
		err := backupRoom(ctx, logger, client, roomID, cli)
		if err != nil {
			// Error is already logged within backupRoom or its helpers
			// Collect errors to report at the end, but continue processing other rooms
			// Log the specific room error here for context at this level
			logger.Error().Str("room_id", roomID.String()).Err(err).Msg("Failed to back up room")
			backupErrors = append(backupErrors, fmt.Errorf("room %s: %w", roomID.String(), err))
		}
	}

	if len(backupErrors) > 0 {
		logger.Error().Int("error_count", len(backupErrors)).Msg("One or more rooms failed to back up completely")
		// Individual errors already logged above
		return errors.New("one or more room backups failed") // Indicate overall failure
	}

	return nil
}
