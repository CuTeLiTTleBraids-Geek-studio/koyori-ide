package services

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestSettingsService builds a SettingsService whose configPath points
// inside a temp dir, so assetsDir() resolves to <tmp>/assets.
func newTestSettingsService(t *testing.T) *SettingsService {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "settings.json")
	return &SettingsService{configPath: configPath}
}

func TestPersonalization_SaveAsset_HappyPath(t *testing.T) {
	svc := newTestSettingsService(t)
	data := []byte("fake-png-bytes")

	rel, err := svc.SavePersonalizationAsset("avatar.png", data)
	if err != nil {
		t.Fatalf("SavePersonalizationAsset: %v", err)
	}
	if rel != "assets/avatar.png" {
		t.Fatalf("expected assets/avatar.png, got %q", rel)
	}

	// File exists on disk at <tmp>/assets/avatar.png
	full := filepath.Join(filepath.Dir(svc.configPath), "assets", "avatar.png")
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read back file: %v", err)
	}
	if string(got) != "fake-png-bytes" {
		t.Fatalf("content mismatch: got %q", got)
	}
}

func TestPersonalization_SaveAsset_PathTraversalSanitized(t *testing.T) {
	svc := newTestSettingsService(t)

	// A traversal-style filename must be reduced to its basename by filepath.Base.
	rel, err := svc.SavePersonalizationAsset("../../etc/passwd", []byte("x"))
	if err != nil {
		t.Fatalf("SavePersonalizationAsset should sanitize, got err: %v", err)
	}
	// The stored file is the basename "passwd", not a traversal.
	if rel != "assets/passwd" {
		t.Fatalf("expected assets/passwd, got %q", rel)
	}
	// Ensure no file was created outside the assets dir.
	etc := filepath.Join(filepath.Dir(svc.configPath), "..", "..", "etc", "passwd")
	if _, err := os.Stat(etc); err == nil {
		t.Fatalf("traversal leaked outside assets dir: %s", etc)
	}
}

func TestPersonalization_SaveAsset_InvalidFilename(t *testing.T) {
	svc := newTestSettingsService(t)
	cases := []string{"", ".", ".."}
	for _, name := range cases {
		if _, err := svc.SavePersonalizationAsset(name, []byte("x")); err == nil {
			t.Fatalf("expected error for filename %q", name)
		}
	}
}

func TestPersonalization_SaveAsset_SizeLimit(t *testing.T) {
	svc := newTestSettingsService(t)
	tooBig := make([]byte, (8<<20)+1)
	if _, err := svc.SavePersonalizationAsset("big.bin", tooBig); err == nil {
		t.Fatal("expected error for asset exceeding 8MB")
	}
}

func TestPersonalization_ReadAsset(t *testing.T) {
	svc := newTestSettingsService(t)
	if _, err := svc.SavePersonalizationAsset("img.png", []byte("hello")); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := svc.ReadPersonalizationAsset("assets/img.png")
	if err != nil {
		t.Fatalf("ReadPersonalizationAsset: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content mismatch: %q", got)
	}

	// Reading a missing asset returns an error.
	if _, err := svc.ReadPersonalizationAsset("assets/missing.png"); err == nil {
		t.Fatal("expected error reading missing asset")
	}
}

func TestPersonalization_DeleteAsset(t *testing.T) {
	svc := newTestSettingsService(t)
	if _, err := svc.SavePersonalizationAsset("to-delete.png", []byte("x")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := svc.DeletePersonalizationAsset("assets/to-delete.png"); err != nil {
		t.Fatalf("DeletePersonalizationAsset: %v", err)
	}
	// Deleting again returns an error (file gone).
	if err := svc.DeletePersonalizationAsset("assets/to-delete.png"); err == nil {
		t.Fatal("expected error deleting missing asset")
	}
}

func TestPersonalization_ConfigRoundTrip(t *testing.T) {
	svc := newTestSettingsService(t)
	original := defaultSettings()
	original.Personalization = &PersonalizationConfig{
		CodeEditorBgImage:   "assets/bg.png",
		CodeEditorBgOpacity: 0.5,
		ChatBgImage:         "assets/chat.png",
		ChatBgBlur:          4,
		UserAvatar:          "assets/user.png",
		AiAvatar:            "assets/ai.png",
		FontFamily:          "Inter",
		FontSize:            15,
		BubbleStyle:         "bubble",
		BubbleOpacity:       0.9,
		MessageSpacing:      16,
	}

	if err := svc.SaveSettings(original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if loaded.Personalization == nil {
		t.Fatal("loaded Personalization is nil")
	}
	p := loaded.Personalization
	if p.CodeEditorBgImage != "assets/bg.png" || p.CodeEditorBgOpacity != 0.5 ||
		p.ChatBgBlur != 4 || p.FontFamily != "Inter" || p.FontSize != 15 ||
		p.BubbleStyle != "bubble" || p.BubbleOpacity != 0.9 || p.MessageSpacing != 16 {
		t.Fatalf("personalization did not round-trip: %+v", p)
	}
}
