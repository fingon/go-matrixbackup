package main

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

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
