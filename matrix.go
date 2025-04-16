package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	if err := os.MkdirAll(roomPath, 0o755); err != nil {
		roomLog.Error().Str("path", roomPath).Err(err).Msg("Failed to create room directory, skipping room")
		return err
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
