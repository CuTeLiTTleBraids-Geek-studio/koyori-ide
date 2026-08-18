package repo

import (
	"os"
	"strings"
	"testing"
)

func TestLinuxPackagingPinsNfpmAndUsesUpdaterCompatibleNames(t *testing.T) {
	script := readRepositoryFile(t, "../../build/scripts/build-linux.sh")
	for _, required := range []string{
		"readonly NFPM_VERSION=\"v2.44.1\"",
		"nfpm@${NFPM_VERSION}",
		"${APP_NAME}-${VERSION}-linux-${ARCH}.deb",
		"${APP_NAME}-${VERSION}-linux-${ARCH}.rpm",
		"GTK_DEB_DEP=\"libgtk-4-1\"",
		"WEBKIT_DEB_DEP=\"libwebkitgtk-6.0-4\"",
		"GTK_DEB_DEP=\"libgtk-3-0\"",
		"WEBKIT_DEB_DEP=\"libwebkit2gtk-4.1-0\"",
		"GTK_RPM_DEP=\"gtk3\"",
		"WEBKIT_RPM_DEP=\"webkit2gtk4.1\"",
		"GTK_ARCH_DEP=\"gtk3\"",
		"WEBKIT_ARCH_DEP=\"webkit2gtk-4.1\"",
		"- ${GTK_DEB_DEP}",
		"- ${WEBKIT_DEB_DEP}",
		"- ${GTK_RPM_DEP}",
		"- ${WEBKIT_RPM_DEP}",
		"- ${GTK_ARCH_DEP}",
		"- ${WEBKIT_ARCH_DEP}",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("build-linux.sh is missing %q", required)
		}
	}
	if !strings.Contains(script, "depends:\n  - ${GTK_DEB_DEP}\n  - ${WEBKIT_DEB_DEP}") {
		t.Error("generated nfpm config must interpolate the detected Debian dependencies")
	}
	if !strings.Contains(script, "rpm:\n    depends:\n      - ${GTK_RPM_DEP}\n      - ${WEBKIT_RPM_DEP}") {
		t.Error("generated nfpm config must interpolate the detected RPM dependencies")
	}
	if !strings.Contains(script, "archlinux:\n    depends:\n      - ${GTK_ARCH_DEP}\n      - ${WEBKIT_ARCH_DEP}") {
		t.Error("generated nfpm config must interpolate the detected Arch dependencies")
	}
	for _, hardcoded := range []string{
		"  - libgtk-4-1\n  - libwebkitgtk-6.0-4",
		"      - gtk4\n      - webkitgtk6.0",
		"      - gtk4\n      - webkitgtk-6.0",
	} {
		if strings.Contains(script, hardcoded) {
			t.Errorf("build-linux.sh still hardcodes a GTK4 dependency block: %q", hardcoded)
		}
	}
	if strings.Contains(script, "nfpm@latest") {
		t.Error("build-linux.sh must not install mutable nfpm@latest")
	}
}

func TestLinuxPackagingDocumentsBothSupportedWebKitStacks(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(readme)
	for _, required := range []string{
		"libgtk-4-dev libwebkitgtk-6.0-dev",
		"libgtk-3-dev libwebkit2gtk-4.1-dev",
		"gtk4-devel webkitgtk6.0-devel",
		"gtk3-devel webkit2gtk4.1-devel",
		"gtk4 webkitgtk-6.0",
		"gtk3 webkit2gtk-4.1",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("README Linux dependency matrix is missing %q", required)
		}
	}

	template := readRepositoryFile(t, "../../build/linux/nfpm/nfpm.yaml")
	for _, required := range []string{
		"GTK4/WebKitGTK 6.0 default",
		"build/scripts/build-linux.sh",
		"build/linux/nfpm/koyori-ide.yaml",
		"Do not package a binary built with the gtk3 tag",
	} {
		if !strings.Contains(template, required) {
			t.Errorf("checked-in nfpm template does not document its stack boundary %q", required)
		}
	}
}

func TestLinuxPackagingFailsClosedAndWorkflowRequiresExactPackages(t *testing.T) {
	script := readRepositoryFile(t, "../../build/scripts/build-linux.sh")
	for _, required := range []string{
		"ensure_nfpm()",
		`fail "nfpm installation failed"`,
		`fail "nfpm was installed but is not available on PATH"`,
		`fail "deb package creation failed: $BIN_DIR/$DEB_FILE"`,
		`[ -s "$BIN_DIR/$DEB_FILE" ] ||`,
		`fail "nfpm did not create the expected deb: $BIN_DIR/$DEB_FILE"`,
		`fail "rpm package creation failed: $BIN_DIR/$RPM_FILE"`,
		`[ -s "$BIN_DIR/$RPM_FILE" ] ||`,
		`fail "nfpm did not create the expected rpm: $BIN_DIR/$RPM_FILE"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("build-linux.sh does not fail closed on required package output %q", required)
		}
	}

	workflow, _ := readReleaseWorkflow(t, "../../.github/workflows/package.yml")
	linuxJob, ok := workflow.Jobs["linux"]
	if !ok {
		t.Fatal("package workflow has no linux job")
	}
	var verification string
	for _, step := range linuxJob.Steps {
		if strings.Contains(step.Run, `expected_deb=`) {
			verification = step.Run
			break
		}
	}
	if verification == "" {
		t.Fatal("package workflow Linux job has no exact package verification step")
	}
	for _, required := range []string{
		`version="$(bash scripts/read-release-version.sh VERSION)"`,
		`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`,
		`expected_deb="bin/koyori-ide-${version}-linux-${{ matrix.arch }}.deb"`,
		`expected_rpm="bin/koyori-ide-${version}-linux-${{ matrix.arch }}.rpm"`,
		`for artifact in "$expected_appimage" "$expected_deb" "$expected_rpm"`,
		`if [ ! -s "$artifact" ]`,
		`dpkg-deb -I "$expected_deb"`,
		`file "$expected_rpm"`,
	} {
		if !strings.Contains(verification, required) {
			t.Errorf("package workflow does not require exact deb/rpm output %q", required)
		}
	}
	for jobName, job := range workflow.Jobs {
		if jobName == "linux" {
			continue
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run, `expected_deb=`) || strings.Contains(step.Run, `expected_rpm=`) {
				t.Errorf("package workflow %s job contains Linux package verification", jobName)
			}
		}
	}
}
