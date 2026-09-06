package services

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSelectUpdateAssetMatchesPlatformBeforeAPIOrder(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assets := []githubReleaseAsset{
		{
			Name:               "attacker-linux-amd64.tar.gz",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/attacker-linux-amd64.tar.gz",
			Digest:             digest,
		},
		{
			Name:               "koyori-ide-v1.0.0-linux-arm64.rpm",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide-v1.0.0-linux-arm64.rpm",
			Digest:             digest,
		},
		{
			Name:               "koyori-ide-v1.0.0-linux-amd64.tar.gz",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide-v1.0.0-linux-amd64.tar.gz",
			Digest:             digest,
		},
	}

	asset, checksum, ok := selectUpdateAsset(assets, "linux", "amd64")
	if !ok {
		t.Fatal("selectUpdateAsset returned no asset")
	}
	if asset.Name != "koyori-ide-v1.0.0-linux-amd64.tar.gz" {
		t.Fatalf("selected asset = %q, want linux amd64 archive", asset.Name)
	}
	if checksum != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("checksum = %q", checksum)
	}
}

func TestSelectUpdateAssetUsesInstallerOnlyAsFallback(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assets := []githubReleaseAsset{{
		Name:               "koyori-ide-v1.0.0-windows-amd64.msi",
		BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide-v1.0.0-windows-amd64.msi",
		Digest:             digest,
	}}

	asset, _, ok := selectUpdateAsset(assets, "windows", "amd64")
	if !ok || asset.Name != "koyori-ide-v1.0.0-windows-amd64.msi" {
		t.Fatalf("selected asset = %+v, ok=%v", asset, ok)
	}
}

func TestSelectUpdateAssetAcceptsConventionalNfpmLinuxNames(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assets := []githubReleaseAsset{
		{
			Name:               "koyori-ide_1.0.0_amd64.deb",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide_1.0.0_amd64.deb",
			Digest:             digest,
		},
		{
			Name:               "koyori-ide-1.0.0-1.x86_64.rpm",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide-1.0.0-1.x86_64.rpm",
			Digest:             digest,
		},
	}

	deb, _, ok := selectUpdateAsset(assets, "linux", "amd64")
	if !ok || deb.Name != "koyori-ide_1.0.0_amd64.deb" {
		t.Fatalf("selected deb = %+v, ok=%v", deb, ok)
	}
	rpmOnly := []githubReleaseAsset{assets[1]}
	rpm, _, ok := selectUpdateAsset(rpmOnly, "linux", "amd64")
	if !ok || rpm.Name != "koyori-ide-1.0.0-1.x86_64.rpm" {
		t.Fatalf("selected rpm = %+v, ok=%v", rpm, ok)
	}
	// The portable archive is preferred when present; with only the native
	// package names above, the Debian fallback must still be architecture-safe.
	arm, _, ok := selectUpdateAsset(assets, "linux", "arm64")
	if ok {
		t.Fatalf("selected an incompatible arm64 asset: %+v", arm)
	}
}

func TestSelectUpdateAssetRejectsAmbiguousOrUntrustedMatches(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assets := []githubReleaseAsset{
		{
			Name:               "koyori-ide-v1.0.0-darwin-amd64.zip",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/a.zip",
			Digest:             digest,
		},
		{
			Name:               "koyori-ide-v1.0.0-darwin-amd64.zip",
			BrowserDownloadURL: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/b.zip",
			Digest:             digest,
		},
		{
			Name:               "koyori-ide-v1.0.0-darwin-amd64.zip",
			BrowserDownloadURL: "https://example.invalid/koyori-ide.zip",
			Digest:             digest,
		},
	}

	if _, _, ok := selectUpdateAsset(assets, "darwin", "amd64"); ok {
		t.Fatal("ambiguous or untrusted matching assets should not be selected")
	}
}

func TestIsSupportedUpdateVersionRequiresStrictSemVer(t *testing.T) {
	for _, version := range []string{
		"0.2.0",
		"1.2.3-beta.1",
		"10.0.0-rc.1",
		"999999999999999999999999.0.1-alpha-1.0",
	} {
		if !isSupportedUpdateVersion(version) {
			t.Errorf("isSupportedUpdateVersion(%q) = false", version)
		}
	}
	for _, version := range []string{
		"beta0.2.0",
		"v1.2.3",
		"1.2",
		"01.2.3",
		"1.02.3",
		"1.2.03",
		"1.2.3-",
		"1.2.3-.alpha",
		"1.2.3-alpha.",
		"1.2.3-alpha..1",
		"1.2.3-01",
		"1.2.3-alpha.01",
		"1.2.3-alpha_beta",
		"1.2.3+build",
	} {
		if isSupportedUpdateVersion(version) {
			t.Errorf("isSupportedUpdateVersion(%q) = true", version)
		}
	}
}

func TestCheckForUpdatesRejectsUnsupportedReleaseTag(t *testing.T) {
	for _, tag := range []string{
		"beta0.2.0",
		"v01.2.3",
		"v1.2.3-",
		"v1.2.3-alpha..1",
		"v1.2.3-01",
		"v1.2.3+build",
	} {
		t.Run(tag, func(t *testing.T) {
			s := NewUpdateService()
			s.setLookupIP(publicGitHubTestResolver)
			s.setHTTPTransport(updateRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"tag_name":"` + tag + `","assets":[]}`)),
				}, nil
			}))

			if _, err := s.CheckForUpdates("0.2.0", ""); err == nil || !strings.Contains(err.Error(), "not a supported semantic version") {
				t.Fatalf("CheckForUpdates error = %v, want unsupported semantic version", err)
			}
		})
	}
}
