package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"gotest.tools/v3/assert"
)

func TestMergeAndValidateConfig(t *testing.T) {
	testCases := []struct {
		name          string
		cliInput      *CLI
		fileInput     *CredentialsFile
		expectedCLI   *CLI
		expectedError string // Substring of the expected error message
	}{
		{
			name: "CLI only - valid",
			cliInput: &CLI{
				Server: "cli_server",
				User:   "cli_user",
				Token:  "cli_token",
			},
			fileInput: nil,
			expectedCLI: &CLI{
				Server: "cli_server",
				User:   "cli_user",
				Token:  "cli_token",
			},
			expectedError: "",
		},
		{
			name:     "File only - valid",
			cliInput: &CLI{},
			fileInput: &CredentialsFile{
				Server:   "file_server",
				User:     "file_user",
				Token:    "file_token",
				DeviceID: "file_device",
			},
			expectedCLI: &CLI{
				Server:   "file_server",
				User:     "file_user",
				Token:    "file_token",
				DeviceID: "file_device",
			},
			expectedError: "",
		},
		{
			name: "CLI overrides File",
			cliInput: &CLI{
				Server: "cli_server",
				User:   "cli_user",
				Token:  "cli_token",
			},
			fileInput: &CredentialsFile{
				Server:   "file_server",
				User:     "file_user",
				Token:    "file_token",
				DeviceID: "file_device",
			},
			expectedCLI: &CLI{
				Server:   "cli_server",
				User:     "cli_user",
				Token:    "cli_token",
				DeviceID: "file_device", // Device ID from file is used as CLI was empty
			},
			expectedError: "",
		},
		{
			name: "Mixed CLI and File",
			cliInput: &CLI{
				Server: "cli_server",
				// User missing
				Token: "cli_token",
			},
			fileInput: &CredentialsFile{
				// Server ignored
				User:     "file_user",
				Token:    "file_token", // Ignored
				DeviceID: "file_device",
			},
			expectedCLI: &CLI{
				Server:   "cli_server",
				User:     "file_user",
				Token:    "cli_token",
				DeviceID: "file_device",
			},
			expectedError: "",
		},
		{
			name:          "Missing all required",
			cliInput:      &CLI{},
			fileInput:     nil,
			expectedCLI:   &CLI{},
			expectedError: "missing required credentials: Server (--server or config file), User (--user or config file), Token (--token or config file)",
		},
		{
			name: "Missing User and Token",
			cliInput: &CLI{
				Server: "cli_server",
			},
			fileInput:     nil,
			expectedCLI:   &CLI{Server: "cli_server"},
			expectedError: "missing required credentials: User (--user or config file), Token (--token or config file)",
		},
		{
			name:     "Missing Token from file",
			cliInput: &CLI{},
			fileInput: &CredentialsFile{
				Server: "file_server",
				User:   "file_user",
				// Token missing
			},
			expectedCLI: &CLI{
				Server: "file_server",
				User:   "file_user",
			},
			expectedError: "missing required credentials: Token (--token or config file)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cliActual := *tc.cliInput // Make a copy to avoid modifying input across tests
			err := mergeAndValidateConfig(&cliActual, tc.fileInput)

			if tc.expectedError == "" {
				assert.NilError(t, err)
				// Use assert.DeepEqual for struct comparison
				assert.DeepEqual(t, &cliActual, tc.expectedCLI)
			} else {
				assert.ErrorContains(t, err, tc.expectedError)
				// Check that the CLI struct wasn't unexpectedly modified beyond merges
				// This comparison might be fragile depending on desired behavior on error
				assert.DeepEqual(t, &cliActual, tc.expectedCLI)
			}
		})
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	logger := zerolog.Nop() // Use a Nop logger for tests

	t.Run("Valid config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		creds := CredentialsFile{
			Server:   "test.server",
			User:     "test_user",
			Token:    "test_token",
			DeviceID: "test_device",
		}
		content, _ := json.Marshal(creds)
		err := os.WriteFile(configPath, content, 0o644)
		assert.NilError(t, err)

		loadedCreds, err := loadConfigFromFile(configPath, logger)
		assert.NilError(t, err)
		assert.Assert(t, loadedCreds != nil)
		assert.Equal(t, loadedCreds.Server, creds.Server)
		assert.Equal(t, loadedCreds.User, creds.User)
		assert.Equal(t, loadedCreds.Token, creds.Token)
		assert.Equal(t, loadedCreds.DeviceID, creds.DeviceID)
	})

	t.Run("File not found", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "nonexistent.json")

		loadedCreds, err := loadConfigFromFile(configPath, logger)
		assert.NilError(t, err) // Should not return error for file not found
		assert.Assert(t, loadedCreds == nil)
	})

	t.Run("Empty path", func(t *testing.T) {
		loadedCreds, err := loadConfigFromFile("", logger)
		assert.NilError(t, err)
		assert.Assert(t, loadedCreds == nil)
	})

	t.Run("Invalid JSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "invalid.json")
		err := os.WriteFile(configPath, []byte("{invalid json"), 0o644)
		assert.NilError(t, err)

		loadedCreds, err := loadConfigFromFile(configPath, logger)
		assert.ErrorContains(t, err, "failed to parse config file")
		assert.Assert(t, loadedCreds == nil)
	})

	t.Run("Permission denied", func(t *testing.T) {
		// This test might be OS-dependent and flaky
		if os.Getuid() == 0 {
			t.Skip("Skipping permission test when running as root")
		}
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "unreadable.json")
		err := os.WriteFile(configPath, []byte("{}"), 0o000) // Write-only
		assert.NilError(t, err)
		// Ensure the file is not readable even if WriteFile succeeded differently than expected
		_ = os.Chmod(configPath, 0o222) // Write-only permissions

		loadedCreds, err := loadConfigFromFile(configPath, logger)
		assert.ErrorContains(t, err, "failed to read config file")
		assert.Assert(t, loadedCreds == nil)
		// Cleanup: make writable so TempDir can remove it
		_ = os.Chmod(configPath, 0o644)
	})
}

func TestLoadAndValidateConfig(t *testing.T) {
	logger := zerolog.Nop()

	t.Run("Valid config file, no CLI flags", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		creds := CredentialsFile{
			Server: "file.server",
			User:   "file_user",
			Token:  "file_token",
		}
		content, _ := json.Marshal(creds)
		err := os.WriteFile(configPath, content, 0o644)
		assert.NilError(t, err)

		cli := &CLI{ConfigFile: configPath}
		err = loadAndValidateConfig(cli, logger)
		assert.NilError(t, err)
		assert.Equal(t, cli.Server, creds.Server)
		assert.Equal(t, cli.User, creds.User)
		assert.Equal(t, cli.Token, creds.Token)
	})

	t.Run("Valid CLI flags, no config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		cli := &CLI{
			ConfigFile: filepath.Join(tmpDir, "nonexistent.json"), // Specify non-existent file
			Server:     "cli.server",
			User:       "cli_user",
			Token:      "cli_token",
		}
		err := loadAndValidateConfig(cli, logger)
		assert.NilError(t, err)
		assert.Equal(t, cli.Server, "cli.server") // Should remain unchanged
		assert.Equal(t, cli.User, "cli_user")
		assert.Equal(t, cli.Token, "cli_token")
	})

	t.Run("CLI flags override config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		fileCreds := CredentialsFile{
			Server: "file.server",
			User:   "file_user",
			Token:  "file_token",
		}
		content, _ := json.Marshal(fileCreds)
		err := os.WriteFile(configPath, content, 0o644)
		assert.NilError(t, err)

		cli := &CLI{
			ConfigFile: configPath,
			Server:     "cli.server", // Override
			User:       "cli_user",   // Override
			Token:      "cli_token",  // Override
		}
		err = loadAndValidateConfig(cli, logger)
		assert.NilError(t, err)
		assert.Equal(t, cli.Server, "cli.server")
		assert.Equal(t, cli.User, "cli_user")
		assert.Equal(t, cli.Token, "cli_token")
	})

	t.Run("Missing required fields (no file, no flags)", func(t *testing.T) {
		tmpDir := t.TempDir()
		cli := &CLI{
			ConfigFile: filepath.Join(tmpDir, "nonexistent.json"),
		}
		err := loadAndValidateConfig(cli, logger)
		assert.ErrorContains(t, err, "missing required credentials")
	})

	t.Run("Missing required field in file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		fileCreds := CredentialsFile{
			Server: "file.server",
			// User missing
			Token: "file_token",
		}
		content, _ := json.Marshal(fileCreds)
		err := os.WriteFile(configPath, content, 0o644)
		assert.NilError(t, err)

		cli := &CLI{ConfigFile: configPath}
		err = loadAndValidateConfig(cli, logger)
		assert.ErrorContains(t, err, "missing required credentials: User")
	})

	t.Run("Invalid JSON in config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "invalid.json")
		err := os.WriteFile(configPath, []byte("{invalid json"), 0o644)
		assert.NilError(t, err)

		cli := &CLI{ConfigFile: configPath}
		err = loadAndValidateConfig(cli, logger)
		assert.ErrorContains(t, err, "failed to parse config file")
	})
}
