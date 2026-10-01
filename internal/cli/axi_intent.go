package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// resolveAxiRunIntent runs before opening run resources. Presence, not value,
// selects the transport: explicit empty input must never become inference or
// an implicit reattach. With neither flag, leave the active-run lookup to decide
// whether intent is required, without touching stdin.
func resolveAxiRunIntent(cmd *cobra.Command, intent, file string) (string, error) {
	hasIntent := cmd.Flags().Changed("intent")
	hasFile := cmd.Flags().Changed("intent-file")
	if hasIntent && hasFile {
		return "", fmt.Errorf("--intent and --intent-file are mutually exclusive")
	}
	if !hasIntent && !hasFile {
		return "", nil
	}

	source := "--intent"
	switch {
	case hasFile:
		if file == "" {
			return "", fmt.Errorf("--intent-file requires a file path")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read --intent-file: %w", err)
		}
		intent, source = string(data), "--intent-file"
	case intent == "-":
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read --intent from stdin: %w", err)
		}
		intent, source = string(data), "--intent stdin"
	}
	// Validate emptiness only. Do not trim the text sent to the run (or the
	// strict-launch digest), especially its leading/trailing whitespace.
	if strings.TrimSpace(intent) == "" {
		return "", fmt.Errorf("%s must not be empty or whitespace-only", source)
	}
	return intent, nil
}
