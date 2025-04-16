package main

import (
	"context"
	"time"

	"github.com/alecthomas/kong"
	"github.com/rs/zerolog/log"
)

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
