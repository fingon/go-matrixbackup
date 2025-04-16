package main

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestSanitizeFilename(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", "_"},
		{"simple", "filename", "filename"},
		{"with spaces", "file name", "file name"},
		{"invalid chars", `fi<l>e:n"a/m\e|?*#`, "fi_l_e_n_a_m_e"},
		{"control chars", "file\x00name\x1F", "file_name"},
		{"leading/trailing spaces", "  filename  ", "filename"},
		{"leading/trailing dots", "..filename..", "filename"},
		{"leading/trailing underscores", "__filename__", "filename"},
		{"mixed leading/trailing", "._ filename _.", "filename"},
		{"multiple underscores", "file___name", "file_name"},
		{"only invalid", `///\\\`, "_"},
		{"unicode", "世界", "世界"},
		{"mixed valid and invalid", "valid<invalid>valid", "valid_invalid_valid"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := sanitizeFilename(tc.input)
			assert.Equal(t, actual, tc.expected)
		})
	}
}
