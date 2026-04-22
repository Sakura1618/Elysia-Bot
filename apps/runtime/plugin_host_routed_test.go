package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimePluginProcessFactoryUsesResolvedAbsoluteExecutablePath(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	executablePath := filepath.Join(workingDirectory, "runtime-launcher.test.exe")
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return executablePath, nil
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: workingDirectory,
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err != nil {
		t.Fatalf("build subprocess command: %v", err)
	}
	if cmd.Path != executablePath {
		t.Fatalf("expected resolved executable path %q, got %q", executablePath, cmd.Path)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("expected launcher path to be absolute, got %q", cmd.Path)
	}
	if cmd.Args[0] != executablePath {
		t.Fatalf("expected resolved executable to be used as argv[0], got %+v", cmd.Args)
	}
	if len(cmd.Args) != 4 || cmd.Args[1] != "-test.run=TestHelperRuntimePluginEchoSubprocess" || cmd.Args[2] != "--" || cmd.Args[3] != "-runtime-plugin-echo-subprocess" {
		t.Fatalf("expected helper-process args built from resolved executable path, got %+v", cmd.Args)
	}
	if cmd.Dir != workingDirectory {
		t.Fatalf("expected guarded working directory %q, got %q", workingDirectory, cmd.Dir)
	}
	foundHelperEnv := false
	for _, entry := range cmd.Env {
		if entry == "GO_WANT_RUNTIME_PLUGIN_ECHO_SUBPROCESS=1" {
			foundHelperEnv = true
			break
		}
	}
	if !foundHelperEnv {
		t.Fatalf("expected subprocess helper env marker, got %+v", cmd.Env)
	}
}

func TestRuntimePluginProcessFactoryFailsClosedWhenExecutableResolutionFails(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return "", errors.New("resolver exploded")
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: workingDirectory,
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err == nil {
		t.Fatal("expected executable resolution failure to block subprocess launch")
	}
	if cmd != nil {
		t.Fatalf("expected no subprocess command on resolution failure, got %+v", cmd)
	}
	if !strings.Contains(err.Error(), "subprocess launch guard blocked start:") || !strings.Contains(err.Error(), "resolver exploded") {
		t.Fatalf("expected fail-closed launch error, got %v", err)
	}
}

func TestCanonicalRuntimeSubprocessExecutablePathReturnsCanonicalAbsolutePath(t *testing.T) {
	t.Parallel()

	resolved, err := canonicalRuntimeSubprocessExecutablePath()
	if err != nil {
		t.Fatalf("resolve canonical executable path: %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("expected canonical executable path to be absolute, got %q", resolved)
	}
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve current executable path: %v", err)
	}
	executablePath, err = filepath.Abs(filepath.Clean(executablePath))
	if err != nil {
		t.Fatalf("resolve absolute executable path: %v", err)
	}
	expected, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		t.Fatalf("resolve expected canonical executable path: %v", err)
	}
	if !reflect.DeepEqual(filepath.Clean(resolved), filepath.Clean(expected)) {
		t.Fatalf("expected canonical executable path %q, got %q", expected, resolved)
	}
}

func TestRuntimePluginProcessFactoryPreservesWorkingDirectoryGuard(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return filepath.Join(workingDirectory, "runtime-launcher.test.exe"), nil
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: filepath.Dir(workingDirectory),
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err == nil {
		t.Fatal("expected working directory guard to fail closed")
	}
	if cmd != nil {
		t.Fatalf("expected no subprocess command when working directory guard rejects launch, got %+v", cmd)
	}
	if !strings.Contains(err.Error(), "subprocess launch guard blocked start:") || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("expected guarded working directory error, got %v", err)
	}
}
