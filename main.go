package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	metadataFilename = "metadata.json"
	dataFilename     = "data.json"
	fetchLimit       = 100 // Number of messages to fetch per request
)

// CredentialsFile defines the structure for the credentials JSON file.
//
// Coincidentally this is same format Matrix-Commander uses
type CredentialsFile struct {
	Server   string `json:"homeserver,omitempty"`
	User     string `json:"user_id,omitempty"`
	Token    string `json:"access_token,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
}

// CLI holds the command-line arguments
type CLI struct {
	// Credentials can be provided via flags or a config file. Flags take precedence.
	// Server, User, and Token are required either via flags or config file.
	Server     string `kong:"name='server',help='Matrix homeserver URL.',group='Credentials'"`
	User       string `kong:"name='user',help='Matrix User ID.',group='Credentials'"`
	Token      string `kong:"name='token',help='Access Token.',group='Credentials'"`
	DeviceID   string `kong:"name='device',help='Device ID (optional).',group='Credentials'"`
	ConfigFile string `kong:"name='config',type='path',default='~/.config/matrix-commander/credentials.json',help='Path to a JSON file containing credentials (server, user, token, device_id). Default: ~/.config/matrix-commander/credentials.json',group='Credentials'"`

	FetchDelay time.Duration `default:"10ms" help:"Delay between requests"`

	// Other options
	BackupDir string `kong:"name='dir',default='./backup',help='Directory to store backups.',group='Options'"`
	Debug     bool   `kong:"name='debug',help='Enable debug logging.'"`
	LogJSON   bool   `kong:"name='log-json',help='Output logs in JSON format.'"`
	Color     bool   `kong:"name='log-color',help='Color logs.'"`
}

type Metadata struct {
	NextToken string `json:"next_token"` // Token to use for the 'from' parameter in the next /messages request
}

// sanitizeFilename removes characters that are problematic in filenames/paths.
var sanitizeRegex = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F#]`)

func sanitizeFilename(name string) string {
	sanitized := sanitizeRegex.ReplaceAllString(name, "_")
	// Replace multiple underscores with a single one
	sanitized = regexp.MustCompile(`_+`).ReplaceAllString(sanitized, "_")
	// Trim leading/trailing underscores/spaces/dots
	sanitized = strings.Trim(sanitized, "_ .")
	if sanitized == "" {
		return "_" // Avoid empty filenames
	}
	return sanitized
}

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

// loadConfigFromFile reads the credentials from the specified JSON file.
// It returns nil if the path is empty or the file doesn't exist.
func loadConfigFromFile(configPath string, logger zerolog.Logger) (*CredentialsFile, error) {
	if configPath == "" {
		return nil, nil // No config file specified
	}

	logger.Info().Str("path", configPath).Msg("Loading credentials from config file")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn().Str("path", configPath).Msg("Config file specified but not found, relying on CLI flags or defaults")
			return nil, nil // File not found is not a fatal error here
		}
		logger.Error().Str("path", configPath).Err(err).Msg("Failed to read config file")
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	var credsFile CredentialsFile
	if err := json.Unmarshal(configData, &credsFile); err != nil {
		logger.Error().Str("path", configPath).Err(err).Msg("Failed to parse config file JSON")
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}
	return &credsFile, nil
}

// mergeAndValidateConfig merges credentials from the file (if provided) into the CLI struct
// giving precedence to values already set in CLI (from flags). It then validates
// that required credentials (Server, User, Token) are present.
func mergeAndValidateConfig(cli *CLI, credsFromFile *CredentialsFile) error {
	// Merge credentials from file if they exist and corresponding CLI flags were not set
	if credsFromFile != nil {
		if cli.Server == "" {
			cli.Server = credsFromFile.Server
		}
		if cli.User == "" {
			cli.User = credsFromFile.User
		}
		if cli.Token == "" {
			cli.Token = credsFromFile.Token
		}
		if cli.DeviceID == "" {
			cli.DeviceID = credsFromFile.DeviceID
		}
	}

	// Validate required credentials after potential merge
	var missing []string
	if cli.Server == "" {
		missing = append(missing, "Server (--server or config file)")
	}
	if cli.User == "" {
		missing = append(missing, "User (--user or config file)")
	}
	if cli.Token == "" {
		missing = append(missing, "Token (--token or config file)")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required credentials: %s", strings.Join(missing, ", "))
	}

	return nil
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

// loadAndValidateConfig loads configuration from file (if specified), merges it with CLI flags,
// and validates that required credentials (Server, User, Token) are present.
func loadAndValidateConfig(cli *CLI, logger zerolog.Logger) error {
	// Attempt to load credentials from the config file.
	credsFromFile, err := loadConfigFromFile(cli.ConfigFile, logger)
	if err != nil {
		// If loading failed (and it wasn't just file not found), return the error.
		return err
	}

	// Merge file credentials (if loaded) with CLI flags and validate the result.
	if err := mergeAndValidateConfig(cli, credsFromFile); err != nil {
		return err
	}

	return nil // Configuration is valid
}

// setupLogging configures the global logger based on CLI flags.
func setupLogging(cli *CLI) zerolog.Logger {
	logLevel := zerolog.InfoLevel
	if cli.Debug {
		logLevel = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(logLevel)
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs // Use milliseconds for timestamp

	var logger zerolog.Logger
	if cli.LogJSON {
		logger = zerolog.New(os.Stderr)
	} else {
		// Pretty console logging
		output := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
		output.NoColor = !cli.Color

		logger = zerolog.New(output)
	}
	logger = logger.With().Timestamp().Logger()

	// Set the global logger instance used by log.Debug(), log.Info(), etc.
	log.Logger = logger

	return logger
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

func main() {
	var cli CLI
	kctx := kong.Parse(&cli)

	logger := setupLogging(&cli)

	// Load and validate configuration
	if err := loadAndValidateConfig(&cli, logger); err != nil {
		// Use the global logger from zerolog/log for fatal errors before full setup might be complete
		log.Fatal().Err(err).Msg("Configuration error")
		// fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err) // Redundant with fatal log
		kctx.Exit(1) // Although Fatal should exit, call this for consistency
	}

	logger.Info().Msg("Starting Matrix backup process...")
	logEvent := logger.Info().Str("server", cli.Server).Str("user", cli.User).Str("backupDir", cli.BackupDir)
	if cli.DeviceID != "" {
		logEvent.Str("device_id", cli.DeviceID)
	}
	logEvent.Msg("Configuration")

	// Initialize Matrix client
	client, err := initializeMatrixClient(&cli, logger)
	if err != nil {
		// Error already logged in initializeMatrixClient
		logger.Fatal().Msg("Initialization failed") // Use Fatal to exit
		kctx.Exit(1)                                // For consistency
	}

	// Backup joined rooms
	err = backupJoinedRooms(context.Background(), client, &cli, logger)
	if err != nil {
		// Specific errors logged within backupJoinedRooms
		logger.Error().Msg("Matrix backup process finished with errors.")
		kctx.Exit(1)
	}
	logger.Info().Msg("Matrix backup process finished successfully.")
}
