//go:build !e2e

package e2e

// Start is intentionally empty in every normal build. The loopback HTTP
// endpoint and its command implementation live in server.go, which the Go
// compiler excludes unless the explicit `e2e` build tag is present. The
// signature mirrors the e2e-tagged Start so main.go does not need build tags.
func Start(_ ServiceSet) (func(), error) {
	return nil, nil
}
