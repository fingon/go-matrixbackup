package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
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
