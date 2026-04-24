package pluginsdk

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckGeneratedManifestMatchesTemplateSmoke(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestFile(t)
	pluginDir := filepath.Join(repoRoot, "plugins", "plugin-template-smoke")
	if err := CheckGeneratedManifest(pluginDir); err != nil {
		t.Fatalf("check generated manifest: %v", err)
	}
}

func TestScaffoldRepoPluginCreatesRepoLocalPluginFlow(t *testing.T) {
	t.Parallel()

	workspaceRoot := createPluginWorkspaceFixture(t)
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "plugins", "plugin-template-smoke", "dist"), 0o755); err != nil {
		t.Fatalf("create template dist junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "plugins", "plugin-template-smoke", "dist", "junk.txt"), []byte("should not scaffold"), 0o644); err != nil {
		t.Fatalf("write template dist junk: %v", err)
	}

	targetDir, err := ScaffoldRepoPlugin(ScaffoldOptions{WorkspaceRoot: workspaceRoot, PluginID: "plugin-sample-alpha"})
	if err != nil {
		t.Fatalf("scaffold plugin: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "manifest_test.go")); err != nil {
		t.Fatalf("expected scaffolded manifest_test.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "template_test.go")); !os.IsNotExist(err) {
		t.Fatalf("expected template_test.go to be renamed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "dist")); !os.IsNotExist(err) {
		t.Fatalf("expected scaffold to skip template dist junk, got err=%v", err)
	}

	manifest, generatedManifest, err := GenerateManifestFromPluginDir(targetDir)
	if err != nil {
		t.Fatalf("generate manifest from scaffolded plugin: %v", err)
	}
	if manifest.ID != "plugin-sample-alpha" {
		t.Fatalf("manifest id = %q, want %q", manifest.ID, "plugin-sample-alpha")
	}
	if manifest.Name != "Plugin Sample Alpha" {
		t.Fatalf("manifest name = %q, want %q", manifest.Name, "Plugin Sample Alpha")
	}
	if manifest.Entry.Module != "plugins/plugin-sample-alpha" {
		t.Fatalf("manifest entry.module = %q, want %q", manifest.Entry.Module, "plugins/plugin-sample-alpha")
	}

	storedManifest, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read scaffolded manifest.json: %v", err)
	}
	if !bytes.Equal(normalizeLineEndings(storedManifest), normalizeLineEndings(generatedManifest)) {
		t.Fatalf("scaffolded manifest.json does not match generated output\n--- stored ---\n%s\n--- generated ---\n%s", storedManifest, generatedManifest)
	}

	if err := CheckGeneratedManifest(targetDir); err != nil {
		t.Fatalf("check scaffolded manifest: %v", err)
	}

	rawGoWork, err := os.ReadFile(filepath.Join(workspaceRoot, "go.work"))
	if err != nil {
		t.Fatalf("read go.work: %v", err)
	}
	if !strings.Contains(string(rawGoWork), "./plugins/plugin-sample-alpha") {
		t.Fatalf("go.work missing scaffolded plugin entry:\n%s", rawGoWork)
	}

	readmeContent, err := os.ReadFile(filepath.Join(targetDir, "README.md"))
	if err != nil {
		t.Fatalf("read scaffolded README: %v", err)
	}
	for _, expected := range []string{"# plugin-sample-alpha", "manifest write -plugin ./plugins/plugin-sample-alpha", "package -plugin ./plugins/plugin-sample-alpha"} {
		if !strings.Contains(string(readmeContent), expected) {
			t.Fatalf("scaffolded README missing %q\n%s", expected, readmeContent)
		}
	}

	command := exec.Command("go", "test", "./...")
	command.Dir = targetDir
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go test scaffolded plugin: %v\n%s", err, string(output))
	}

	distDir, err := PackagePlugin(targetDir)
	if err != nil {
		t.Fatalf("package scaffolded plugin: %v", err)
	}
	for _, fileName := range []string{"manifest.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(distDir, fileName)); err != nil {
			t.Fatalf("expected dist artifact %s: %v", fileName, err)
		}
	}
	if _, err := os.Stat(filepath.Join(distDir, "go.mod")); !os.IsNotExist(err) {
		t.Fatalf("expected package output to omit dist/go.mod, got err=%v", err)
	}

	distManifest, err := os.ReadFile(filepath.Join(distDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read dist manifest: %v", err)
	}
	if !bytes.Equal(normalizeLineEndings(distManifest), normalizeLineEndings(generatedManifest)) {
		t.Fatalf("dist manifest does not match generated manifest\n--- dist ---\n%s\n--- generated ---\n%s", distManifest, generatedManifest)
	}
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()

	_, currentFilePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}

	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return repoRoot
}

func createPluginWorkspaceFixture(t *testing.T) string {
	t.Helper()

	repoRoot := repoRootFromTestFile(t)
	workspaceRoot := t.TempDir()

	goWork := "go 1.25.0\n\nuse (\n\t./packages/event-model\n\t./packages/plugin-sdk\n\t./packages/runtime-core\n\t./plugins/plugin-template-smoke\n)\n"
	if err := os.WriteFile(filepath.Join(workspaceRoot, "go.work"), []byte(goWork), 0o644); err != nil {
		t.Fatalf("write fixture go.work: %v", err)
	}

	copyDirOrFail := func(sourceRelativePath string) {
		sourcePath := filepath.Join(repoRoot, filepath.FromSlash(sourceRelativePath))
		targetPath := filepath.Join(workspaceRoot, filepath.FromSlash(sourceRelativePath))
		if err := copyDir(sourcePath, targetPath); err != nil {
			t.Fatalf("copy %s: %v", sourceRelativePath, err)
		}
	}

	copyDirOrFail("packages/event-model")
	copyDirOrFail("packages/plugin-sdk")
	copyDirOrFail("packages/runtime-core")
	copyDirOrFail("plugins/plugin-template-smoke")

	return workspaceRoot
}
