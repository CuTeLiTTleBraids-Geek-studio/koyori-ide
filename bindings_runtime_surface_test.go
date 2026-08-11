package main

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type bindingManifestSurface struct {
	Exports map[string][]string `json:"exports"`
}

var wailsInternalServiceMethods = map[string]bool{
	"ServiceName":     true,
	"ServiceStartup":  true,
	"ServiceShutdown": true,
	"ServeHTTP":       true,
}

var forbiddenWailsRuntimeMethods = map[string]map[string]bool{
	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/settingsservice.ts": {
		"DeleteExtensionSecret": true,
		"DeleteSecret":          true,
		"GetExtensionSecret":    true,
		"GetSecret":             true,
		"StoreExtensionSecret":  true,
		"StoreSecret":           true,
	},
	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/toolchainservice.ts": {
		"SetToolPaths": true,
	},
}

func TestRegisteredWailsRuntimeSurfaceMatchesManifest(t *testing.T) {
	source, err := os.ReadFile("scripts/wails-bindings.manifest.json")
	if err != nil {
		t.Fatalf("read binding manifest: %v", err)
	}
	var manifest bindingManifestSurface
	if err := json.Unmarshal(source, &manifest); err != nil {
		t.Fatalf("parse binding manifest: %v", err)
	}

	seen := make(map[string]bool)
	var unexpected []string
	for _, service := range (&appBundle{}).wailsServices() {
		serviceType := reflect.TypeOf(service.Instance())
		if serviceType == nil || serviceType.Kind() != reflect.Pointer {
			t.Fatalf("registered Wails service has invalid type %v", serviceType)
		}
		namedType := serviceType.Elem()
		module := namedType.PkgPath() + "/" + strings.ToLower(namedType.Name()) + ".ts"
		allowed, ok := manifest.Exports[module]
		if !ok {
			t.Errorf("registered Wails service %s has no manifest module %s", namedType, module)
			continue
		}
		seen[module] = true
		allowedSet := make(map[string]bool, len(allowed))
		for _, method := range allowed {
			allowedSet[method] = true
		}
		for index := 0; index < serviceType.NumMethod(); index++ {
			method := serviceType.Method(index).Name
			if forbiddenWailsRuntimeMethods[module][method] {
				unexpected = append(unexpected, module+":"+method+" (forbidden)")
				continue
			}
			if !wailsInternalServiceMethods[method] && !allowedSet[method] {
				unexpected = append(unexpected, module+":"+method)
			}
		}
	}

	for module := range manifest.Exports {
		if strings.HasPrefix(module, "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/") && !seen[module] {
			t.Errorf("manifest service module is not registered at runtime: %s", module)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		t.Fatalf(
			"Wails runtime exposes methods outside the generated allowlist:\n%s",
			strings.Join(unexpected, "\n"),
		)
	}
}
