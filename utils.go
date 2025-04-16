package main

import (
	"regexp"
	"strings"
)

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
