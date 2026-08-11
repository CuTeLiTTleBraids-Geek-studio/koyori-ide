package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDebugService_G14_RealDelveNestedVariables drives the real Delve DAP
// adapter (I-level evidence for GOAL P9-G14): breakpoint → stop → nested
// variables expanded through adapter-owned references → single step → stop.
// Skipped when dlv is not on PATH so the suite stays portable.
func TestDebugService_G14_RealDelveNestedVariables(t *testing.T) {
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Skip("dlv not on PATH; real Delve adapter test skipped")
	}
	_ = dlv

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module g14fixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainSrc := `package main

import "fmt"

type Inner struct {
	Z int
}

type Outer struct {
	Name string
	In   Inner
}

func main() {
	o := Outer{Name: "hello", In: Inner{Z: 42}}
	fmt.Println(o)
}
`
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	bpLine := 0
	for i, line := range strings.Split(mainSrc, "\n") {
		if strings.Contains(line, "fmt.Println(o)") {
			bpLine = i + 1
			break
		}
	}
	if bpLine == 0 {
		t.Fatal("could not locate breakpoint line")
	}

	d := NewDebugService()
	t.Cleanup(func() { _ = d.Stop() })
	if _, err := d.SetBreakpointEx(mainPath, bpLine, "", ""); err != nil {
		t.Fatalf("SetBreakpointEx: %v", err)
	}
	info, err := d.LaunchPackage(dir)
	if err != nil {
		t.Fatalf("LaunchPackage (real dlv): %v", err)
	}
	if info.Address == "" {
		t.Fatalf("expected dlv dap address, got %+v", info)
	}
	if info.AdapterID != "delve" || info.SourcePackID != "org.koyori.ide.go" || info.SourcePackVersion != "1.0.0" {
		t.Fatalf("Delve language pack source metadata is missing: %+v", info)
	}

	waitStopped(t, d, 30*time.Second)
	if err := d.RefreshStackAndLocals(); err != nil {
		t.Fatalf("RefreshStackAndLocals: %v", err)
	}

	st := d.GetState()
	var outer *DebugVariable
	for i := range st.Locals {
		v := &st.Locals[i]
		if v.Name == "o" {
			outer = v
			break
		}
	}
	if outer == nil {
		t.Fatalf("no variable o in locals: %+v", st.Locals)
	}
	if outer.VariablesReference <= 0 {
		t.Fatalf("Outer variable has no adapter-owned reference: %+v", outer)
	}

	fields, err := d.GetVariables(outer.VariablesReference, 0, 0)
	if err != nil {
		t.Fatalf("GetVariables(outer): %v", err)
	}
	var inner *DebugVariable
	for i := range fields {
		v := &fields[i]
		if v.Name == "In" {
			inner = v
			break
		}
	}
	if inner == nil {
		t.Fatalf("nested In not found: %+v", fields)
	}
	if inner.VariablesReference <= 0 {
		t.Fatalf("nested In has no adapter-owned reference: %+v", inner)
	}

	innerFields, err := d.GetVariables(inner.VariablesReference, 0, 0)
	if err != nil {
		t.Fatalf("GetVariables(inner): %v", err)
	}
	var z *DebugVariable
	for i := range innerFields {
		v := &innerFields[i]
		if v.Name == "Z" {
			z = v
			break
		}
	}
	if z == nil || z.Value != "42" {
		t.Fatalf("Z not 42: %+v", innerFields)
	}

	if err := d.StepOver(); err != nil {
		t.Fatalf("StepOver: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	advanced := false
	for time.Now().Before(deadline) {
		st = d.GetState()
		if st.Session.Stopped && st.StopReason != "entry" {
			advanced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !advanced {
		t.Fatalf("StepOver did not produce a new stop: %+v", st)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !d.GetState().Session.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Println("real Delve G14 fixture OK")
}

// TestDebugService_G14_RealNodeCDPNestedVariables drives the real Node.js
// CDP inspector adapter (I-level evidence for GOAL P9-G14, second adapter):
// stop-at-entry → continue to a `debugger` statement → nested object expanded
// through connection-scoped variablesReferences → single step → stop.
// Skipped when node is not on PATH.
func TestDebugService_G14_RealNodeCDPNestedVariables(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; real Node CDP adapter test skipped")
	}
	_ = nodeBin

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"type\":\"module\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "script.js")
	script := `const inner = { z: 42 };
const outer = { name: "hi", inner };
debugger;
console.log(outer.name);
`
	if err := os.WriteFile(prog, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	d := NewDebugService()
	t.Cleanup(func() { _ = d.Stop() })
	info, err := d.LaunchNode(prog, nil)
	if err != nil {
		t.Fatalf("LaunchNode (real node): %v", err)
	}
	if !info.Stopped || info.StopReason != "entry" {
		t.Fatalf("expected stop-at-entry, got %+v", info)
	}
	if info.AdapterID != "node-inspector" || info.SourcePackID != "org.koyori.ide.typescript" || info.SourcePackVersion != "1.0.0" {
		t.Fatalf("Node language pack source metadata is missing: %+v", info)
	}

	// launchNode marks stop-at-entry optimistically; wait until the real CDP
	// paused event has populated the stack before resuming, otherwise the
	// adapter rejects Debugger.resume ("Can only perform operation while
	// paused").
	entryDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(entryDeadline) {
		if len(d.GetState().Stack) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(d.GetState().Stack) == 0 {
		t.Fatal("CDP paused event never populated the stack")
	}

	if err := d.Continue(); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	// Node/CDP mode: the paused event handler populates stack+locals directly
	// (no DAP RefreshStackAndLocals). Wait for the debugger-statement stop.
	stopDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(stopDeadline) {
		st := d.GetState()
		if st.Session.Stopped && len(st.Locals) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	st := d.GetState()
	var outer *DebugVariable
	for i := range st.Locals {
		v := &st.Locals[i]
		if v.Name == "outer" {
			outer = v
			break
		}
	}
	if outer == nil {
		t.Fatalf("no variable outer in locals: %+v", st.Locals)
	}
	if outer.VariablesReference <= 0 {
		t.Fatalf("Node outer object has no variablesReference: %+v", outer)
	}

	fields, err := d.GetVariables(outer.VariablesReference, 0, 0)
	if err != nil {
		t.Fatalf("GetVariables(outer) via Node CDP: %v", err)
	}
	var inner *DebugVariable
	for i := range fields {
		v := &fields[i]
		if v.Name == "inner" {
			inner = v
			break
		}
	}
	if inner == nil || inner.VariablesReference <= 0 {
		t.Fatalf("nested inner object missing/ref=0: %+v", fields)
	}

	innerFields, err := d.GetVariables(inner.VariablesReference, 0, 0)
	if err != nil {
		t.Fatalf("GetVariables(inner) via Node CDP: %v", err)
	}
	var z *DebugVariable
	for i := range innerFields {
		v := &innerFields[i]
		if v.Name == "z" {
			z = v
			break
		}
	}
	if z == nil || z.Value != "42" {
		t.Fatalf("z != 42 via Node CDP: %+v", innerFields)
	}

	if err := d.StepOver(); err != nil {
		t.Fatalf("StepOver: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	advanced := false
	for time.Now().Before(deadline) {
		st = d.GetState()
		if st.Session.Stopped {
			advanced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !advanced {
		t.Fatalf("StepOver did not produce a stop: %+v", st)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
