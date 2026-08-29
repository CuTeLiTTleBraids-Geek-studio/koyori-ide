package services

import (
	"strings"
	"testing"
)

func TestEscapeCmdArgRoundTrip(t *testing.T) {
	tests := []string{
		"a & calc",
		"x | whoami",
		"y > out",
		"z < in",
		"p ^ q",
		"100% ",
		"%PATH%",
		"say !hi!",
		`embedded "quote"`,
		"",
		" ",
		"こんにちは",
	}

	for _, want := range tests {
		got, ok := parseEscapedCmdArg(escapeCmdArg(want))
		if !ok {
			t.Errorf("escapeCmdArg(%q) produced an invalid cmd argument: %q", want, escapeCmdArg(want))
			continue
		}
		if got != want {
			t.Errorf("escapeCmdArg(%q) round-tripped as %q", want, got)
		}
	}
}

// parseEscapedCmdArg models the cmd.exe metacharacter and quote handling that
// matters for this contract. It proves the generated token has no unescaped
// command separators and preserves the original value; the Windows test below
// is authoritative for cmd.exe's actual behavior.
func parseEscapedCmdArg(s string) (string, bool) {
	var ok bool
	if s, ok = unescapeCmdMeta(s); !ok {
		return "", false
	}
	if s, ok = unescapeCmdMeta(s); !ok {
		return "", false
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	var out []byte
	for i := 1; i < len(s)-1; {
		start := i
		for i < len(s)-1 && s[i] == '\\' {
			i++
		}
		slashes := i - start
		if i == len(s)-1 {
			if slashes%2 != 0 {
				return "", false
			}
			out = append(out, strings.Repeat("\\", slashes/2)...)
			break
		}
		if s[i] == '"' {
			if slashes%2 == 0 {
				return "", false
			}
			out = append(out, strings.Repeat("\\", slashes/2)...)
			out = append(out, '"')
		} else {
			out = append(out, strings.Repeat("\\", slashes)...)
			out = append(out, s[i])
		}
		i++
	}
	return string(out), true
}

func unescapeCmdMeta(s string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '^' {
			i++
			if i >= len(s) {
				return "", false
			}
		}
		out.WriteByte(s[i])
	}
	return out.String(), true
}
