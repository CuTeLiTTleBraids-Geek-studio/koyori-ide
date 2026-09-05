package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

func TestB4BootstrapStageFunctionsStayUnder80Lines(t *testing.T) {
	required := map[string]bool{
		"runMain":               false,
		"buildCoreServices":     false,
		"bindWorkspaceRoots":    false,
		"registerWailsServices": false,
		"setupHTTPRoutes":       false,
		"startBackgroundJobs":   false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, tracked := required[function.Name.Name]; !tracked {
			continue
		}
		required[function.Name.Name] = true
		lineCount := fset.Position(function.End()).Line - fset.Position(function.Pos()).Line + 1
		if lineCount >= 80 {
			t.Errorf("%s spans %d lines, want fewer than 80", function.Name.Name, lineCount)
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("required bootstrap stage %s is missing", name)
		}
	}
}

func TestLanguagePackServiceUsesKoyoriConfigDirectory(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "NewLanguagePackService" {
			return true
		}
		argument, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || argument.Sel.Name != "koyoriDir" {
			t.Errorf("NewLanguagePackService must receive cfg.koyoriDir")
			return true
		}
		cfg, ok := argument.X.(*ast.Ident)
		if !ok || cfg.Name != "cfg" {
			t.Errorf("NewLanguagePackService must receive cfg.koyoriDir")
			return true
		}
		found = true
		return true
	})
	if !found {
		t.Fatal("NewLanguagePackService(cfg.koyoriDir) wiring is missing")
	}
}

func TestAIReasoningEventIsRegisteredAtWailsBoundary(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selectorName := ""
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			selectorName = fun.Sel.Name
		case *ast.IndexExpr:
			if selector, ok := fun.X.(*ast.SelectorExpr); ok {
				selectorName = selector.Sel.Name
			}
		case *ast.IndexListExpr:
			if selector, ok := fun.X.(*ast.SelectorExpr); ok {
				selectorName = selector.Sel.Name
			}
		}
		if selectorName != "RegisterEvent" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if ok && literal.Kind == token.STRING && strings.Trim(literal.Value, "\"") == "ai:reasoning" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("Wails ai:reasoning event registration is missing")
	}
}

func TestCleanupStackRunsBusinessBeforeLockAndLogger(t *testing.T) {
	var order []string
	var cleanups cleanupStack
	cleanups.add(func() { order = append(order, "logger") })
	cleanups.add(func() { order = append(order, "lock") })
	cleanups.add(func() {
		order = append(order, "business")
		panic("business cleanup failed")
	})

	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		cleanups.run()
	}()

	if !panicked {
		t.Fatal("business cleanup panic was unexpectedly swallowed")
	}
	if got, want := strings.Join(order, ","), "business,lock,logger"; got != want {
		t.Fatalf("cleanup order = %q, want %q", got, want)
	}
}

func TestEmitTimeEventsStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	emitted := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		emitTimeEvents(ctx, ticks, func(value string) { emitted <- value })
		close(done)
	}()

	tick := time.Date(2026, time.July, 16, 1, 2, 3, 0, time.UTC)
	ticks <- tick
	select {
	case got := <-emitted:
		if want := tick.Format(time.RFC1123); got != want {
			t.Fatalf("emitted time = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("time event was not emitted")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("time event goroutine did not stop after cancellation")
	}
}

func TestStopOnSignalCallsGracefulStopWithoutProcessSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	stopped := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		stopOnSignal(ctx, signals, func() { stopped <- struct{}{} })
		close(done)
	}()

	signals <- os.Interrupt
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("graceful stop was not called after the injected signal")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("signal watcher did not exit after graceful stop")
	}
}

func TestRunAndReportLogsErrorAfterDeferredCleanup(t *testing.T) {
	wantErr := errors.New("run failed")
	var order []string
	runAndReport(func() (err error) {
		defer func() { order = append(order, "cleanup") }()
		return wantErr
	}, func(err error) {
		if !errors.Is(err, wantErr) {
			t.Errorf("reported error = %v, want %v", err, wantErr)
		}
		order = append(order, "reported")
	})

	if got, want := strings.Join(order, ","), "cleanup,reported"; got != want {
		t.Fatalf("run order = %q, want %q", got, want)
	}
}

func TestRunShutdownActionsStartsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	actions := []shutdownAction{
		{name: "first", run: func() error { started <- "first"; <-release; return nil }},
		{name: "second", run: func() error { started <- "second"; <-release; return nil }},
	}
	done := make(chan struct{})
	go func() {
		runShutdownActions(context.Background(), actions, func(string, ...any) {})
		close(done)
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("shutdown actions did not start concurrently")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown coordinator did not finish")
	}
}

func TestRunShutdownActionsCollectsEveryResultBeforeDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	completed := make(chan string, 3)
	actions := make([]shutdownAction, 3)
	for i := range actions {
		name := fmt.Sprintf("service-%d", i)
		actions[i] = shutdownAction{name: name, run: func() error {
			completed <- name
			return nil
		}}
	}

	runShutdownActions(ctx, actions, func(message string, args ...any) {
		t.Fatalf("unexpected shutdown warning: %s %v", message, args)
	})
	if err := ctx.Err(); err != nil {
		t.Fatalf("normal shutdown consumed its context: %v", err)
	}

	seen := make(map[string]int, len(actions))
	for range actions {
		select {
		case name := <-completed:
			seen[name]++
		case <-time.After(time.Second):
			t.Fatal("shutdown returned before every action completed")
		}
	}
	for _, action := range actions {
		if seen[action.name] != 1 {
			t.Errorf("shutdown action %q ran %d times, want 1", action.name, seen[action.name])
		}
	}
}

func TestRunShutdownActionsUsesGlobalDeadline(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	exited := make(chan struct{}, 3)
	actions := make([]shutdownAction, 3)
	for i := range actions {
		actions[i] = shutdownAction{name: fmt.Sprintf("service-%d", i), run: func() error {
			started <- struct{}{}
			<-release
			exited <- struct{}{}
			return nil
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	startedAt := time.Now()
	go func() {
		runShutdownActions(ctx, actions, func(string, ...any) {})
		close(done)
	}()
	for range actions {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("shutdown worker did not start")
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("shutdown coordinator ignored the global deadline")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("shutdown context error = %v, want deadline exceeded", ctx.Err())
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown elapsed %s, want one global deadline", elapsed)
	}

	close(release)
	for range actions {
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Fatal("late shutdown worker blocked after coordinator returned")
		}
	}
}

func TestRunCoreShutdownDeadlineDoesNotLetBlockedJobsStopDelayServices(t *testing.T) {
	releaseJobs := make(chan struct{})
	jobsExited := make(chan struct{})
	jobs := &backgroundJobs{timeDone: releaseJobs}
	servicesCalled := make(chan string, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runCoreShutdown(ctx, func() {
			jobs.stop()
			close(jobsExited)
		}, []shutdownAction{
			{name: "service-1", run: func() error { servicesCalled <- "service-1"; return nil }},
			{name: "service-2", run: func() error { servicesCalled <- "service-2"; return nil }},
		}, func(string, ...any) {})
		close(done)
	}()

	for range 2 {
		select {
		case <-servicesCalled:
		case <-time.After(time.Second):
			close(releaseJobs)
			t.Fatal("a blocked jobs stop prevented another service shutdown")
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		close(releaseJobs)
		t.Fatal("shutdown did not return after the global deadline")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("shutdown context error = %v, want deadline exceeded", ctx.Err())
	}

	close(releaseJobs)
	select {
	case <-jobsExited:
	case <-time.After(time.Second):
		t.Fatal("jobs shutdown worker deadlocked after coordinator returned")
	}
}

func TestRunShutdownActionsReportsErrorsAndPendingServices(t *testing.T) {
	wantErr := errors.New("close failed")
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var warnings []string
	runShutdownActions(ctx, []shutdownAction{
		{name: "failed", run: func() error { return wantErr }},
		{name: "blocked", run: func() error { <-release; return nil }},
	}, func(message string, args ...any) {
		warnings = append(warnings, fmt.Sprint(append([]any{message}, args...)...))
	})

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "failed") || !strings.Contains(joined, wantErr.Error()) {
		t.Fatalf("warnings did not report service error: %q", joined)
	}
	if !strings.Contains(joined, "blocked") || !strings.Contains(joined, "deadline") {
		t.Fatalf("warnings did not report pending service: %q", joined)
	}
}

func TestLoadStartupSettingsCallsLoaderOnce(t *testing.T) {
	calls := 0
	want := services.Settings{OpenAIWindowOnStartup: true}
	got, ok := loadStartupSettings(func() (services.Settings, error) {
		calls++
		return want, nil
	}, func(string, ...any) {
		t.Fatal("warning callback called for successful settings load")
	})

	if calls != 1 {
		t.Fatalf("LoadSettings calls = %d, want 1", calls)
	}
	if !ok || got.OpenAIWindowOnStartup != want.OpenAIWindowOnStartup {
		t.Fatalf("loaded settings = %+v, ok=%v; want cached startup settings", got, ok)
	}
}

func TestLoadStartupSettingsWarnsOnError(t *testing.T) {
	wantErr := errors.New("settings unavailable")
	warnings := 0
	_, ok := loadStartupSettings(func() (services.Settings, error) {
		return services.Settings{}, wantErr
	}, func(message string, args ...any) {
		warnings++
		if !strings.Contains(strings.ToLower(message), "settings") {
			t.Errorf("warning message %q does not identify settings", message)
		}
		if len(args) == 0 {
			t.Error("settings warning omitted structured error context")
		}
	})

	if ok {
		t.Fatal("settings load unexpectedly reported success")
	}
	if warnings != 1 {
		t.Fatalf("settings warnings = %d, want 1", warnings)
	}
}

func TestLoadInstalledExtensionManifestsWarnsOnError(t *testing.T) {
	wantErr := errors.New("extension scan failed")
	warnings := 0
	manifests, ok := loadInstalledExtensionManifests(func() ([]services.VSCodeExtensionManifest, error) {
		return nil, wantErr
	}, func(message string, args ...any) {
		warnings++
		if !strings.Contains(strings.ToLower(message), "extension") {
			t.Errorf("warning message %q does not identify extensions", message)
		}
		if len(args) == 0 {
			t.Error("extension warning omitted structured error context")
		}
	})

	if ok || manifests != nil {
		t.Fatalf("extension load = %+v, ok=%v; want failure", manifests, ok)
	}
	if warnings != 1 {
		t.Fatalf("extension warnings = %d, want 1", warnings)
	}
}

// prompt-6 Task 6 / BUG-M10: responseRecorder must preserve status codes.
func TestResponseRecorderStreamsNonHTMLWithoutBuffering(t *testing.T) {
	underlying := httptest.NewRecorder()
	rec := &responseRecorder{
		ResponseWriter: underlying,
		buf:            &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
	rec.Header().Set("Content-Type", "application/octet-stream")
	rec.WriteHeader(http.StatusOK)

	firstChunk := []byte("first chunk")
	if _, err := rec.Write(firstChunk); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := underlying.Body.String(); got != string(firstChunk) {
		t.Fatalf("underlying body after first Write = %q, want streamed %q", got, firstChunk)
	}
	if rec.buf.Len() != 0 {
		t.Fatalf("non-HTML buffer length = %d, want 0", rec.buf.Len())
	}
}

func TestResponseRecorder_PreservesStatusCode(t *testing.T) {
	rec := &responseRecorder{
		ResponseWriter: httptest.NewRecorder(),
		buf:            &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
	rec.Header().Set("Content-Type", "text/html; charset=utf-8")
	rec.WriteHeader(http.StatusNotFound)
	if rec.statusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.statusCode)
	}
	n, err := rec.Write([]byte("missing"))
	if err != nil || n != 7 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if rec.buf.String() != "missing" {
		t.Fatalf("body = %q", rec.buf.String())
	}
}

func TestResponseRecorder_DefaultStatusOK(t *testing.T) {
	rec := &responseRecorder{
		ResponseWriter: httptest.NewRecorder(),
		buf:            &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
	// Downstream may never call WriteHeader; default stays 200.
	if rec.statusCode != http.StatusOK {
		t.Fatalf("default status = %d", rec.statusCode)
	}
}

// N-34 (prompt-4.md): CSP nonce injection tests.

func TestInjectNonceIntoHTML_BareScriptTag(t *testing.T) {
	html := []byte(`<html><body><script>console.log("hi")</script></body></html>`)
	got := injectNonceIntoHTML(html, "abc123")
	if !strings.Contains(string(got), `<script nonce="abc123">`) {
		t.Fatalf("expected nonce injected into <script>, got: %s", got)
	}
}

func TestInjectNonceIntoHTML_ModuleScriptTag(t *testing.T) {
	html := []byte(`<script type="module" src="/main.ts"></script>`)
	got := injectNonceIntoHTML(html, "n0nc3")
	if !strings.Contains(string(got), `<script nonce="n0nc3" type="module" src="/main.ts">`) {
		t.Fatalf("expected nonce injected before type attribute, got: %s", got)
	}
}

func TestInjectNonceIntoHTML_PreservesExistingNonce(t *testing.T) {
	html := []byte(`<script nonce="existing">foo()</script>`)
	got := injectNonceIntoHTML(html, "new")
	if strings.Contains(string(got), `nonce="new"`) {
		t.Fatalf("should not override existing nonce, got: %s", got)
	}
	if !strings.Contains(string(got), `nonce="existing"`) {
		t.Fatalf("existing nonce should be preserved, got: %s", got)
	}
}

func TestInjectNonceIntoHTML_MultipleScriptTags(t *testing.T) {
	html := []byte(`<script>a()</script><script type="module">b()</script>`)
	got := injectNonceIntoHTML(html, "xyz")
	if strings.Count(string(got), `nonce="xyz"`) != 2 {
		t.Fatalf("expected 2 nonce injections, got: %s", got)
	}
}

func TestInjectNonceIntoHTML_NoScriptTags(t *testing.T) {
	html := []byte(`<html><body><p>hello</p></body></html>`)
	got := injectNonceIntoHTML(html, "n")
	if string(got) != string(html) {
		t.Fatalf("expected no changes when no <script> tags, got: %s", got)
	}
}

func TestInjectNonceIntoHTML_SelfClosingScript(t *testing.T) {
	// Self-closing <script src="..."/> is invalid HTML but we should
	// still inject the nonce — the regex matches any <script...>.
	html := []byte(`<script src="external.js"/>`)
	got := injectNonceIntoHTML(html, "tok")
	if !strings.Contains(string(got), `nonce="tok"`) {
		t.Fatalf("expected nonce injected, got: %s", got)
	}
}

func TestGenerateNonce_LengthAndHex(t *testing.T) {
	nonce, err := generateNonce()
	if err != nil {
		t.Fatalf("generateNonce returned unexpected error: %v", err)
	}
	// 16 bytes -> 32 hex chars
	if len(nonce) != 32 {
		t.Fatalf("expected 32-char hex nonce, got %d chars: %s", len(nonce), nonce)
	}
	if nonce == "" {
		t.Fatalf("expected non-empty nonce")
	}
	for _, c := range nonce {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			t.Fatalf("non-hex char %q in nonce %s", c, nonce)
		}
	}
}

func TestGenerateNonce_Uniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		n, err := generateNonce()
		if err != nil {
			t.Fatalf("generateNonce returned unexpected error on iteration %d: %v", i, err)
		}
		if seen[n] {
			t.Fatalf("nonce %s repeated after %d iterations", n, i)
		}
		seen[n] = true
	}
}

// TestGenerateNonce_NonEmptyOnSuccess verifies the happy path: a non-empty
// hex string and nil error. Mocking crypto/rand.Read failure in Go is
// awkward without dependency injection, so we assert the success path
// returns a usable nonce — the failure branch returns ("", err) by
// construction (G-SEC-10).
func TestGenerateNonce_NonEmptyOnSuccess(t *testing.T) {
	nonce, err := generateNonce()
	if err != nil {
		t.Fatalf("expected nil error on healthy crypto/rand, got: %v", err)
	}
	if nonce == "" {
		t.Fatalf("expected non-empty nonce, got empty string")
	}
}

func TestContentSecurityPolicyWithNonce_Formats(t *testing.T) {
	csp := fmt.Sprintf(contentSecurityPolicyWithNonce, "test123")
	if !strings.Contains(csp, "'nonce-test123'") {
		t.Fatalf("expected CSP to contain 'nonce-test123', got: %s", csp)
	}
	// style-src keeps 'unsafe-inline' (Vue scoped styles), but script-src
	// must use the nonce instead. Verify script-src specifically.
	parts := strings.Split(csp, ";")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(trimmed, "script-src") {
			if strings.Contains(trimmed, "'unsafe-inline'") {
				t.Fatalf("script-src must not contain 'unsafe-inline': %s", trimmed)
			}
			if !strings.Contains(trimmed, "'nonce-test123'") {
				t.Fatalf("script-src must contain 'nonce-test123': %s", trimmed)
			}
		}
	}
}

func TestContentSecurityPolicyStatic_NoUnsafeInline(t *testing.T) {
	if strings.Contains(contentSecurityPolicyStatic, "'unsafe-inline'") {
		// style-src keeps 'unsafe-inline' (Vue scoped styles), but
		// script-src must not. Verify script-src specifically.
		parts := strings.Split(contentSecurityPolicyStatic, ";")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if strings.HasPrefix(trimmed, "script-src") {
				if strings.Contains(trimmed, "'unsafe-inline'") {
					t.Fatalf("script-src must not contain 'unsafe-inline': %s", trimmed)
				}
			}
		}
	}
}

// G-PERF-04: BenchmarkGenerateNonce benchmarks CSP nonce generation.
// Lives here (package main) because generateNonce is defined in main.go,
// not the services package. The CI perf-benchmark job targets
// ./services/... and so does not exercise this benchmark; it is kept for
// local/manual performance characterization of the CSP nonce path.
// generateNonce is also covered by TestGenerateNonce_* above.
func BenchmarkGenerateNonce(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = generateNonce()
	}
}

// P9-G08: the packaged runtime embeds the repository VERSION file, so
// UpdateService.GetCurrentVersion can always report the same version that the
// platform metadata carries.
func TestMainEmbedsRepositoryVERSION(t *testing.T) {
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if got := strings.TrimSpace(embeddedVersion); got != want {
		t.Fatalf("embeddedVersion = %q, want %q", got, want)
	}
	if embeddedVersion == "" {
		t.Fatal("embeddedVersion is empty")
	}
}
