package main

import (
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

func TestSplitCommandKeepsFlagValuesOpaque(t *testing.T) {
	flags, command, err := splitCommand([]string{
		"read", "--workspace", "catalog", "--state-dir=read", "--path", "read",
	})
	if err != nil {
		t.Fatalf("splitCommand: %v", err)
	}
	if command != "read" {
		t.Fatalf("command = %q, want read", command)
	}
	want := []string{"--workspace", "catalog", "--state-dir=read", "--path", "read"}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flags = %#v, want %#v", flags, want)
	}
}

func TestSplitCommandKeepsSingleDashFlagValuesOpaque(t *testing.T) {
	flags, command, err := splitCommand([]string{
		"read", "-workspace", "catalog", "-state-dir", "read", "-path", "catalog",
	})
	if err != nil {
		t.Fatalf("splitCommand: %v", err)
	}
	if command != "read" {
		t.Fatalf("command = %q, want read", command)
	}
	want := []string{"-workspace", "catalog", "-state-dir", "read", "-path", "catalog"}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flags = %#v, want %#v", flags, want)
	}
}

func TestSplitCommandRejectsAmbiguousCommands(t *testing.T) {
	_, _, err := splitCommand([]string{"catalog", "read"})
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("splitCommand error = %v, want ErrInvalidInput", err)
	}
}

func TestSplitCommandRejectsDuplicateAuthorityFlagsAndSeparator(t *testing.T) {
	tests := [][]string{
		{"read", "--workspace", "first", "-workspace", "second"},
		{"read", "--state-dir=first", "--state-dir", "second"},
		{"read", "-path=first", "--path=second"},
		{"--workspace", "workspace", "--", "catalog"},
	}
	for _, args := range tests {
		if _, _, err := splitCommand(args); !errors.Is(err, services.ErrInvalidInput) {
			t.Fatalf("splitCommand(%#v) error = %v, want ErrInvalidInput", args, err)
		}
	}
}

func TestRunRejectsEmptyPathFlagForCatalog(t *testing.T) {
	err := run([]string{
		"--workspace", t.TempDir(), "--state-dir", t.TempDir(), "--path=", "catalog",
	}, io.Discard)
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("run catalog with empty path flag error = %v, want ErrInvalidInput", err)
	}
}

func TestSplitCommandRecognizesExternalReceiptCommandsAndKeepsValuesOpaque(t *testing.T) {
	flags, command, err := splitCommand([]string{
		"external-receipt-dispose", "--workspace", "external-receipts", "--state-dir", "read",
		"--handle", "receipt-recovery-v1:abc", "--disposition", "manual-unknown",
	})
	if err != nil {
		t.Fatalf("splitCommand: %v", err)
	}
	if command != "external-receipt-dispose" {
		t.Fatalf("command = %q, want external-receipt-dispose", command)
	}
	want := []string{"--workspace", "external-receipts", "--state-dir", "read", "--handle", "receipt-recovery-v1:abc", "--disposition", "manual-unknown"}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flags = %#v, want %#v", flags, want)
	}
}

func TestRunRejectsNonCanonicalExternalReceiptDisposition(t *testing.T) {
	err := run([]string{
		"--workspace", t.TempDir(), "--state-dir", t.TempDir(),
		"external-receipt-dispose", "--handle", "receipt-recovery-v1:abc",
		"--disposition", " manual-unknown ",
	}, io.Discard)
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("non-canonical disposition error = %v, want ErrInvalidInput", err)
	}
}

func TestRunRejectsAuthorityFlagsForExternalReceiptInventory(t *testing.T) {
	err := run([]string{
		"--workspace", t.TempDir(), "--state-dir", t.TempDir(),
		"external-receipts", "--handle", "receipt-recovery-v1:abc",
	}, io.Discard)
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("inventory handle flag error = %v, want ErrInvalidInput", err)
	}
}

func TestRunRejectsNonCanonicalExternalReceiptDispositionBeforeOpeningState(t *testing.T) {
	err := run([]string{
		"--workspace", t.TempDir(), "--state-dir", t.TempDir(),
		"external-receipt-dispose", "--handle", "receipt-recovery-v1:invalid",
		"--disposition", " manual-unknown ",
	}, io.Discard)
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("non-canonical external receipt disposition error = %v, want ErrInvalidInput", err)
	}
}

func TestRunRejectsRecoveryFlagsForCatalog(t *testing.T) {
	err := run([]string{
		"--workspace", t.TempDir(), "--state-dir", t.TempDir(), "catalog",
		"--disposition", "manual-unknown",
	}, io.Discard)
	if !errors.Is(err, services.ErrInvalidInput) {
		t.Fatalf("catalog recovery flag error = %v, want ErrInvalidInput", err)
	}
}
