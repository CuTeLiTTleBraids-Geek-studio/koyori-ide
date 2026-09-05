package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

type installSpec struct {
	File      string
	SHA256    string
	Publisher string
	Name      string
	Version   string
}

func main() {
	configDir := flag.String("config", "", "temporary MarketplaceService config directory")
	specPath := flag.String("spec", "", "JSON array of VSIX install specifications")
	flag.Parse()
	if *configDir == "" || *specPath == "" {
		fmt.Fprintln(os.Stderr, "-config and -spec are required")
		os.Exit(2)
	}
	data, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read spec: %v\n", err)
		os.Exit(1)
	}
	var specs []installSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		fmt.Fprintf(os.Stderr, "parse spec: %v\n", err)
		os.Exit(1)
	}
	svc := services.NewMarketplaceService(*configDir)
	for _, spec := range specs {
		if err := svc.InstallVSIXFile(spec.File, spec.SHA256, spec.Publisher, spec.Name, spec.Version); err != nil {
			fmt.Fprintf(os.Stderr, "install %s.%s: %v\n", spec.Publisher, spec.Name, err)
			os.Exit(1)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"installed": len(specs)}); err != nil {
		fmt.Fprintf(os.Stderr, "write result: %v\n", err)
		os.Exit(1)
	}
}
