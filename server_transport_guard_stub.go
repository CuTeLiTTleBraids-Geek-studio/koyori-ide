//go:build !server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func serverTransportMiddleware() application.Middleware { return nil }
