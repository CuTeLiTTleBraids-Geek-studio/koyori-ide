package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

type cliOutput struct {
	Success          bool                                            `json:"success"`
	Catalog          *services.HeadlessAgentCatalog                  `json:"catalog,omitempty"`
	Read             *services.HeadlessReadResult                    `json:"read,omitempty"`
	ExternalReceipts []services.AgentExternalReceiptRecoveryEntry    `json:"externalReceipts,omitempty"`
	ExternalReceipt  *services.AgentExternalReceiptDispositionResult `json:"externalReceipt,omitempty"`
	Code             string                                          `json:"code,omitempty"`
}

func main() {
	// Service constructors may use the process-wide fallback logger for desktop
	// diagnostics. A CI-facing CLI must never let those messages bypass its
	// bounded JSON error categories and disclose local paths or receipt state.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := run(os.Args[1:], os.Stdout); err != nil {
		// Keep diagnostics intentionally bounded and category-only.  The
		// service layer may know paths, receipt IDs, or ledger details, but a
		// CI consumer must never echo those values to its caller.
		_ = writeJSON(os.Stdout, cliOutput{Success: false, Code: classifyError(err)})
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) (returnErr error) {
	flagArgs, command, err := splitCommand(args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("agentcli", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("workspace", "", "absolute workspace directory")
	stateDir := flags.String("state-dir", "", "absolute state directory")
	pathValue := flags.String("path", "", "workspace-relative path")
	handleValue := flags.String("handle", "", "opaque external receipt recovery handle")
	dispositionValue := flags.String("disposition", "", "external receipt disposition")
	if err := flags.Parse(flagArgs); err != nil {
		return fmt.Errorf("invalid command line: %w", services.ErrInvalidInput)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %w", services.ErrInvalidInput)
	}
	if strings.TrimSpace(*workspace) == "" || strings.TrimSpace(*stateDir) == "" || command == "" {
		return fmt.Errorf("workspace, state-dir, and one command are required: %w", services.ErrInvalidInput)
	}
	if command != "catalog" && command != "read" && command != "external-receipts" && command != "external-receipt-dispose" {
		return fmt.Errorf("unsupported headless command: %w", services.ErrInvalidInput)
	}
	switch command {
	case "read":
		if strings.TrimSpace(*pathValue) == "" {
			return fmt.Errorf("read path is required: %w", services.ErrInvalidInput)
		}
		if authorityFlagProvided(flagArgs, "handle") || authorityFlagProvided(flagArgs, "disposition") {
			return fmt.Errorf("read does not accept recovery flags: %w", services.ErrInvalidInput)
		}
	case "catalog":
		if authorityFlagProvided(flagArgs, "path") || authorityFlagProvided(flagArgs, "handle") || authorityFlagProvided(flagArgs, "disposition") {
			return fmt.Errorf("catalog does not accept path or recovery flags: %w", services.ErrInvalidInput)
		}
	case "external-receipts":
		if authorityFlagProvided(flagArgs, "path") || authorityFlagProvided(flagArgs, "handle") || authorityFlagProvided(flagArgs, "disposition") {
			return fmt.Errorf("external-receipts does not accept path or disposition flags: %w", services.ErrInvalidInput)
		}
	case "external-receipt-dispose":
		if authorityFlagProvided(flagArgs, "path") {
			return fmt.Errorf("external-receipt-dispose does not accept a path: %w", services.ErrInvalidInput)
		}
		if strings.TrimSpace(*handleValue) == "" {
			return fmt.Errorf("external receipt handle is required: %w", services.ErrInvalidInput)
		}
		if *dispositionValue != "manual-unknown" {
			return fmt.Errorf("external receipt disposition must be manual-unknown: %w", services.ErrInvalidInput)
		}
	}

	host, err := services.NewHeadlessAgentHost(*workspace, *stateDir)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			if closeErr := host.Close(); closeErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close headless state: %w", services.ErrUsagePersistence))
			}
		}
	}()

	ctx := context.Background()
	var response cliOutput
	switch command {
	case "catalog":
		catalog, catalogErr := host.Catalog(ctx)
		if catalogErr != nil {
			return catalogErr
		}
		response = cliOutput{Success: true, Catalog: &catalog}
	case "read":
		result, readErr := host.Read(ctx, *pathValue)
		if readErr != nil {
			return readErr
		}
		response = cliOutput{Success: true, Read: &result}
	case "external-receipts":
		entries, recoveryErr := host.PendingExternalReceiptDispositions()
		if recoveryErr != nil {
			return recoveryErr
		}
		response = cliOutput{Success: true, ExternalReceipts: entries}
	case "external-receipt-dispose":
		result, recoveryErr := host.DispatchExternalReceiptDisposition(services.AgentExternalReceiptDispositionRequest{
			Handle: *handleValue, Disposition: *dispositionValue,
		})
		if recoveryErr != nil {
			return recoveryErr
		}
		response = cliOutput{Success: true, ExternalReceipt: &result}
	default:
		return fmt.Errorf("unsupported headless command: %w", services.ErrInvalidInput)
	}
	if err := host.Close(); err != nil {
		return fmt.Errorf("close headless state: %w", services.ErrUsagePersistence)
	}
	closed = true
	return writeJSON(output, response)
}

// splitCommand accepts both `read --path file` and `--path file read`, while
// keeping flag values (including a value literally named "read") opaque.
func splitCommand(args []string) ([]string, string, error) {
	flagArgs := make([]string, 0, len(args))
	command := ""
	valueFlags := map[string]string{
		"--workspace": "workspace", "--state-dir": "state-dir", "--path": "path",
		"--handle": "handle", "--disposition": "disposition",
		"-workspace": "workspace", "-state-dir": "state-dir", "-path": "path",
		"-handle": "handle", "-disposition": "disposition",
	}
	seenFlags := make(map[string]struct{}, len(valueFlags)/2)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return nil, "", fmt.Errorf("the -- separator is not supported: %w", services.ErrInvalidInput)
		}
		if arg == "catalog" || arg == "read" || arg == "external-receipts" || arg == "external-receipt-dispose" {
			if command != "" {
				return nil, "", fmt.Errorf("multiple headless commands are not allowed: %w", services.ErrInvalidInput)
			}
			command = arg
			continue
		}
		flagArgs = append(flagArgs, arg)
		name := arg
		if equal := strings.IndexByte(name, '='); equal >= 0 {
			name = name[:equal]
		}
		canonical, hasValue := valueFlags[name]
		if hasValue {
			if _, duplicate := seenFlags[canonical]; duplicate {
				return nil, "", fmt.Errorf("flag %s may be specified only once: %w", name, services.ErrInvalidInput)
			}
			seenFlags[canonical] = struct{}{}
		}
		if hasValue && !strings.ContainsRune(arg, '=') {
			if index+1 >= len(args) {
				return nil, "", fmt.Errorf("flag %s requires a value: %w", name, services.ErrInvalidInput)
			}
			index++
			flagArgs = append(flagArgs, args[index])
		}
	}
	return flagArgs, command, nil
}

func authorityFlagProvided(args []string, canonical string) bool {
	for _, arg := range args {
		name := arg
		if equal := strings.IndexByte(name, '='); equal >= 0 {
			name = name[:equal]
		}
		switch canonical {
		case "workspace":
			if name == "--workspace" || name == "-workspace" {
				return true
			}
		case "state-dir":
			if name == "--state-dir" || name == "-state-dir" {
				return true
			}
		case "path":
			if name == "--path" || name == "-path" {
				return true
			}
		case "handle":
			if name == "--handle" || name == "-handle" {
				return true
			}
		case "disposition":
			if name == "--disposition" || name == "-disposition" {
				return true
			}
		}
	}
	return false
}

func writeJSON(output io.Writer, value cliOutput) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(value)
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, services.ErrUsagePersistence),
		errors.Is(err, services.ErrUsagePersistenceIndeterminate),
		errors.Is(err, services.ErrUsagePersistencePoisoned),
		errors.Is(err, services.ErrUsageReceiptState),
		errors.Is(err, services.ErrAgentRecoveryPersistence),
		errors.Is(err, services.ErrAgentRecoveryPersistenceIndeterminate):
		return "usage-unavailable"
	case errors.Is(err, services.ErrInvalidInput):
		return "invalid-input"
	case errors.Is(err, services.ErrNotAllowed):
		return "operation-rejected"
	default:
		return "operation-failed"
	}
}
