package repo

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestServerDeploymentRequiresAuthenticatedBoundary(t *testing.T) {
	goMod := readRepositoryFile(t, "../../go.mod")
	if !strings.Contains(goMod, "github.com/wailsapp/wails/v3 v3.0.0-beta.8") {
		t.Error("server deployment must use Wails beta.8+, whose server WebSocket transport enforces same-origin checks")
	}
	dockerfile := readRepositoryFile(t, "../../build/docker/Dockerfile.server")
	for _, required := range []string{
		"FROM golang:1.25-alpine@sha256:",
		"FROM node:20.19-alpine@sha256:",
		"ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:",
		"RUN cd frontend && npm ci --ignore-scripts && npm run build",
		"RUN go mod download && go mod verify",
		"CGO_ENABLED=1 requires explicit compatible GO_IMAGE and RUNTIME_IMAGE overrides",
		"WAILS_SERVER_HOST=127.0.0.1",
		"COPY --from=builder /app/server-gateway /server-gateway",
		"ENTRYPOINT [\"/server-gateway\"]",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile.server is missing %q", required)
		}
	}
	if strings.Contains(dockerfile, "RUN go mod tidy") {
		t.Error("Dockerfile.server must not rewrite go.mod/go.sum during an image build")
	}
	if strings.Contains(dockerfile, "ENTRYPOINT [\"/server\"]") || strings.Contains(dockerfile, "WAILS_SERVER_HOST=0.0.0.0") {
		t.Error("Dockerfile.server retains a raw network-exposed Wails entrypoint")
	}
	for name, minimumUses := range map[string]int{"GO_IMAGE": 3, "RUNTIME_IMAGE": 2} {
		match := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindStringSubmatch(dockerfile)
		if len(match) != 2 || strings.Count(dockerfile, match[1]) < minimumUses {
			t.Errorf("Dockerfile.server must reuse and validate the %s default digest", name)
		}
	}

	taskfile := readRepositoryFile(t, "../../build/Taskfile.yml")
	for _, required := range []string{
		"-p {{.HOST_IP | default \"127.0.0.1\"}}:{{.PORT | default \"8080\"}}:8080",
		"KOYORI_SERVER_TOKEN",
		"-e KOYORI_EXTERNAL_ORIGIN",
		"{{if .GO_IMAGE}}--build-arg GO_IMAGE={{.GO_IMAGE}}{{end}}",
		"{{if .RUNTIME_IMAGE}}--build-arg RUNTIME_IMAGE={{.RUNTIME_IMAGE}}{{end}}",
		"server-gateway --check-env-token",
	} {
		if !strings.Contains(taskfile, required) {
			t.Errorf("build/Taskfile.yml is missing %q", required)
		}
	}
	if !strings.Contains(taskfile, `HOST_IP: "{{.HOST_IP}}"`) {
		t.Error("Docker task must expose the host bind address as an explicit variable")
	}
	if strings.Contains(taskfile, `GO_IMAGE=golang:bookworm`) || strings.Contains(taskfile, `RUNTIME_IMAGE=gcr.io/distroless/base-debian12:nonroot`) || strings.Contains(taskfile, `GO_IMAGE | default`) {
		t.Error("Docker task must not document mutable image overrides or duplicate Dockerfile image defaults")
	}
	serverDoc := readRepositoryFile(t, "../../build/docker/SERVER.md")
	for _, required := range []string{
		"KOYORI_EXTERNAL_ORIGIN=https://ide.example.com",
		"HOST_IP=127.0.0.1 task run:docker",
		"--mount type=bind,src=/absolute/path/tls.crt",
		"Required public HTTPS origin",
	} {
		if !strings.Contains(serverDoc, required) {
			t.Errorf("server deployment documentation is missing %q", required)
		}
	}
}

func TestServerInstallersPinTheirAdvertisedPortAndBind(t *testing.T) {
	for _, relative := range []string{
		"../../build/scripts/create-offline-installers.ps1",
		"../../build/scripts/wsl-package-all.sh",
		"../../build/scripts/wsl-repack-only.sh",
	} {
		content := readRepositoryFile(t, relative)
		if !strings.Contains(content, "34115") {
			t.Errorf("%s does not advertise the expected server port", relative)
			continue
		}
		if !strings.Contains(content, "WAILS_SERVER_HOST=127.0.0.1 WAILS_SERVER_PORT=34115") {
			t.Errorf("%s advertises 34115 without forcing loopback and matching Wails port", relative)
		}
	}
}

func readRepositoryFile(t *testing.T, relative string) string {
	t.Helper()
	content, err := os.ReadFile(relative)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
