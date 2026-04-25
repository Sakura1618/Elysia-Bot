package pluginsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const manifestProbeOutputPrefix = "PLUGIN_DEV_MANIFEST="

var scaffoldPluginIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ScaffoldOptions struct {
	WorkspaceRoot string
	PluginID      string
	PluginName    string
}

func DiscoverWorkspaceRoot(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		return "", errors.New("start path is required")
	}

	resolved, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root from %q: %w", start, err)
	}

	info, err := os.Stat(resolved)
	if err == nil && !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}

	for {
		candidate := filepath.Join(resolved, "go.work")
		if _, err := os.Stat(candidate); err == nil {
			return resolved, nil
		}

		parent := filepath.Dir(resolved)
		if parent == resolved {
			break
		}
		resolved = parent
	}

	return "", fmt.Errorf("workspace root not found from %q", start)
}

func MarshalManifest(manifest PluginManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate manifest: %w", err)
	}

	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}

	return append(rawManifest, '\n'), nil
}

func GenerateManifestFromPluginDir(pluginDir string) (PluginManifest, []byte, error) {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return PluginManifest{}, nil, fmt.Errorf("resolve plugin directory: %w", err)
	}

	packageName, err := detectPackageName(resolvedPluginDir)
	if err != nil {
		return PluginManifest{}, nil, err
	}

	tempProbePath := filepath.Join(resolvedPluginDir, "plugindev_manifest_probe_test.go")
	tempProbeFile, err := os.CreateTemp(resolvedPluginDir, "plugindev_manifest_probe_*_test.go")
	if err != nil {
		return PluginManifest{}, nil, fmt.Errorf("create manifest probe: %w", err)
	}
	tempProbePath = tempProbeFile.Name()
	probeTestName := toPascalCase(strings.TrimSuffix(filepath.Base(tempProbePath), filepath.Ext(tempProbePath)))
	probeSource := fmt.Sprintf("package %s\n\nimport (\n\t\"encoding/json\"\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc Test%s(t *testing.T) {\n\trawManifest, err := json.Marshal(Manifest())\n\tif err != nil {\n\t\tt.Fatalf(\"marshal manifest: %%v\", err)\n\t}\n\tfmt.Printf(\"%s%%s\\n\", rawManifest)\n}\n", packageName, probeTestName, manifestProbeOutputPrefix)
	if _, err := tempProbeFile.Write([]byte(probeSource)); err != nil {
		_ = tempProbeFile.Close()
		_ = os.Remove(tempProbePath)
		return PluginManifest{}, nil, fmt.Errorf("write manifest probe: %w", err)
	}
	if err := tempProbeFile.Close(); err != nil {
		_ = os.Remove(tempProbePath)
		return PluginManifest{}, nil, fmt.Errorf("close manifest probe: %w", err)
	}
	defer os.Remove(tempProbePath)

	output, err := runGoCommand(resolvedPluginDir, "test", "-run", "^Test"+probeTestName+"$", "-count=1", "-v")
	if err != nil {
		return PluginManifest{}, nil, fmt.Errorf("load manifest from plugin source: %w\n%s", err, string(output))
	}

	rawManifest, err := extractManifestProbeOutput(output)
	if err != nil {
		return PluginManifest{}, nil, err
	}

	var manifest PluginManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return PluginManifest{}, nil, fmt.Errorf("unmarshal generated manifest: %w", err)
	}

	formattedManifest, err := MarshalManifest(manifest)
	if err != nil {
		return PluginManifest{}, nil, err
	}

	return manifest, formattedManifest, nil
}

func WriteGeneratedManifest(pluginDir string) (string, error) {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return "", fmt.Errorf("resolve plugin directory: %w", err)
	}

	_, rawManifest, err := GenerateManifestFromPluginDir(resolvedPluginDir)
	if err != nil {
		return "", err
	}

	manifestPath := filepath.Join(resolvedPluginDir, "manifest.json")
	if err := os.WriteFile(manifestPath, rawManifest, 0o644); err != nil {
		return "", fmt.Errorf("write manifest.json: %w", err)
	}

	return manifestPath, nil
}

func CheckGeneratedManifest(pluginDir string) error {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return fmt.Errorf("resolve plugin directory: %w", err)
	}

	_, generatedManifest, err := GenerateManifestFromPluginDir(resolvedPluginDir)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(resolvedPluginDir, "manifest.json")
	storedManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest.json: %w", err)
	}

	if !bytes.Equal(normalizeLineEndings(storedManifest), normalizeLineEndings(generatedManifest)) {
		return fmt.Errorf("manifest.json is out of date in %s; run plugin-dev manifest write -plugin %s", resolvedPluginDir, resolvedPluginDir)
	}

	return nil
}

func PackagePlugin(pluginDir string) (string, error) {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return "", fmt.Errorf("resolve plugin directory: %w", err)
	}

	_, generatedManifest, err := GenerateManifestFromPluginDir(resolvedPluginDir)
	if err != nil {
		return "", err
	}

	distDir := filepath.Join(resolvedPluginDir, "dist")
	if err := os.RemoveAll(distDir); err != nil {
		return "", fmt.Errorf("reset dist directory: %w", err)
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return "", fmt.Errorf("create dist directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(distDir, "manifest.json"), generatedManifest, 0o644); err != nil {
		return "", fmt.Errorf("write dist manifest: %w", err)
	}

	for _, fileName := range []string{"README.md"} {
		sourcePath := filepath.Join(resolvedPluginDir, fileName)
		if _, err := os.Stat(sourcePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("stat %s: %w", sourcePath, err)
		}
		if err := copyFile(sourcePath, filepath.Join(distDir, fileName)); err != nil {
			return "", err
		}
	}

	return distDir, nil
}

func SmokePlugin(pluginDir string) (string, error) {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return "", fmt.Errorf("resolve plugin directory: %w", err)
	}

	if err := CheckGeneratedManifest(resolvedPluginDir); err != nil {
		return "", fmt.Errorf("manifest check: %w", err)
	}

	distDir, err := PackagePlugin(resolvedPluginDir)
	if err != nil {
		return "", fmt.Errorf("package plugin: %w", err)
	}

	if err := runPluginTests(resolvedPluginDir); err != nil {
		return "", err
	}

	return distDir, nil
}

func ScaffoldRepoPlugin(options ScaffoldOptions) (string, error) {
	workspaceRoot := strings.TrimSpace(options.WorkspaceRoot)
	if workspaceRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working directory: %w", err)
		}
		workspaceRoot, err = DiscoverWorkspaceRoot(cwd)
		if err != nil {
			return "", err
		}
	}

	resolvedWorkspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}

	pluginID := strings.TrimSpace(options.PluginID)
	if err := validateScaffoldPluginID(pluginID); err != nil {
		return "", err
	}

	pluginName := strings.TrimSpace(options.PluginName)
	if pluginName == "" {
		pluginName = derivePluginName(pluginID)
	}

	templateDir := filepath.Join(resolvedWorkspaceRoot, "plugins", "plugin-template-smoke")
	targetDir := filepath.Join(resolvedWorkspaceRoot, "plugins", pluginID)
	if _, err := os.Stat(targetDir); err == nil {
		return "", fmt.Errorf("target plugin directory already exists: %s", targetDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat target plugin directory: %w", err)
	}

	if err := copyDir(templateDir, targetDir); err != nil {
		return "", err
	}
	cleanupTarget := true
	defer func() {
		if cleanupTarget {
			_ = os.RemoveAll(targetDir)
		}
	}()

	replacements := scaffoldReplacements(pluginID, pluginName)
	rewriteTargets, err := filepath.Glob(filepath.Join(targetDir, "*.go"))
	if err != nil {
		return "", fmt.Errorf("glob scaffolded Go files: %w", err)
	}
	sort.Strings(rewriteTargets)
	rewriteTargets = append(rewriteTargets, filepath.Join(targetDir, "go.mod"))
	for _, rewriteTarget := range rewriteTargets {
		if err := rewriteFile(rewriteTarget, replacements); err != nil {
			return "", err
		}
	}

	if err := os.WriteFile(filepath.Join(targetDir, "README.md"), []byte(scaffoldReadme(pluginID)), 0o644); err != nil {
		return "", fmt.Errorf("write scaffold README: %w", err)
	}

	if _, err := WriteGeneratedManifest(targetDir); err != nil {
		return "", err
	}

	if err := updateGoWorkUseEntry(resolvedWorkspaceRoot, filepath.ToSlash(filepath.Join(".", "plugins", pluginID))); err != nil {
		return "", err
	}

	cleanupTarget = false
	return targetDir, nil
}

func detectPackageName(pluginDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(pluginDir, "*.go"))
	if err != nil {
		return "", fmt.Errorf("glob Go files in %s: %w", pluginDir, err)
	}
	sort.Strings(matches)

	for _, match := range matches {
		if strings.HasSuffix(match, "_test.go") {
			continue
		}
		parsedFile, err := parser.ParseFile(token.NewFileSet(), match, nil, parser.PackageClauseOnly)
		if err != nil {
			return "", fmt.Errorf("parse package name from %s: %w", match, err)
		}
		return parsedFile.Name.Name, nil
	}

	return "", fmt.Errorf("no non-test Go files found in %s", pluginDir)
}

func extractManifestProbeOutput(output []byte) ([]byte, error) {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, manifestProbeOutputPrefix) {
			return []byte(strings.TrimPrefix(line, manifestProbeOutputPrefix)), nil
		}
	}

	return nil, fmt.Errorf("generated manifest output marker not found")
}

func normalizeLineEndings(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func validateScaffoldPluginID(pluginID string) error {
	if strings.TrimSpace(pluginID) == "" {
		return errors.New("plugin id is required")
	}
	if strings.Contains(pluginID, "/") || strings.Contains(pluginID, "\\") {
		return fmt.Errorf("plugin id %q must be a single repo-local directory name", pluginID)
	}
	if !scaffoldPluginIDPattern.MatchString(pluginID) {
		return fmt.Errorf("plugin id %q must use lowercase letters, digits, and hyphens", pluginID)
	}
	if !strings.HasPrefix(pluginID, "plugin-") {
		return fmt.Errorf("plugin id %q must start with %q", pluginID, "plugin-")
	}
	return nil
}

func derivePluginName(pluginID string) string {
	trimmedID := strings.TrimPrefix(pluginID, "plugin-")
	if trimmedID == "" {
		trimmedID = pluginID
	}

	parts := strings.Split(trimmedID, "-")
	for index, part := range parts {
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}

	return "Plugin " + strings.Join(parts, " ")
}

func scaffoldReplacements(pluginID, pluginName string) []stringReplacement {
	pluginSlug := strings.TrimPrefix(pluginID, "plugin-")
	if pluginSlug == "" {
		pluginSlug = pluginID
	}

	pascalID := toPascalCase(pluginID)
	return []stringReplacement{
		{old: "github.com/ohmyopencode/bot-platform/plugins/plugin-template-smoke", new: "github.com/ohmyopencode/bot-platform/plugins/" + pluginID},
		{old: "TestTemplateManifestConstantsStayInSync", new: "Test" + pascalID + "ManifestArtifactsStayInSync"},
		{old: "TestPluginTemplateSmoke", new: "Test" + pascalID},
		{old: "plugins/plugin-template-smoke", new: "plugins/" + pluginID},
		{old: "plugintemplatesmoke", new: toPackageName(pluginID)},
		{old: "TemplatePlugin", new: pascalID},
		{old: "Plugin Template Smoke", new: pluginName},
		{old: "plugin-template-smoke", new: pluginID},
		{old: "template: ", new: pluginID + ": "},
		{old: "evt-template-smoke", new: "evt-" + pluginSlug},
		{old: "trace-template-smoke", new: "trace-" + pluginSlug},
		{old: "onebot:msg:template-smoke", new: "onebot:msg:" + pluginSlug},
		{old: "evt-template", new: "evt-" + pluginSlug},
		{old: "trace-template", new: "trace-" + pluginSlug},
		{old: "onebot:msg:template", new: "onebot:msg:" + pluginSlug},
	}
}

type stringReplacement struct {
	old string
	new string
}

func rewriteFile(path string, replacements []stringReplacement) error {
	rawFile, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	content := string(rawFile)
	for _, replacement := range replacements {
		content = strings.ReplaceAll(content, replacement.old, replacement.new)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func scaffoldReadme(pluginID string) string {
	return fmt.Sprintf("# %s\n\n`%s` 由 `plugins/plugin-template-smoke` 通过当前仓库内的 `plugin-dev` 脚手架生成。\n\n## 当前开发工作流\n\n1. 修改 `plugin.go` 中的业务逻辑与 `Manifest()`。\n2. 如果 `Manifest()` 有变化，运行 `npm run plugin:manifest:write -- -plugin ./plugins/%s` 刷新 `manifest.json`。\n3. 运行 `npm run plugin:smoke -- -plugin ./plugins/%s`，按 `manifest check -> package -> go test` 验证当前插件。\n4. 如需只查看本地轻量打包产物，仍可单独运行 `npm run plugin:package -- -plugin ./plugins/%s`，产物会写到 `plugins/%s/dist/`。\n\n## 说明\n\n- `manifest.json` 是生成物，不再手工维护；`plugin:smoke` 会先做 `manifest check`，所以忘记刷新时会直接失败。\n- `manifest_test.go` 会校验 `Manifest()`、`manifest.json` 与 `go.mod` 中的 module / entry.module 仍保持同步。\n- 如果要把这个插件接入 runtime 或 e2e smoke，请按实际需要单独注册；当前脚手架不自动扩展 runtime 装配路径。\n", pluginID, pluginID, pluginID, pluginID, pluginID, pluginID)
}

func runPluginTests(pluginDir string) error {
	resolvedPluginDir, err := filepath.Abs(strings.TrimSpace(pluginDir))
	if err != nil {
		return fmt.Errorf("resolve plugin directory: %w", err)
	}

	output, err := runGoCommand(resolvedPluginDir, "test", "-count=1", "./...")
	if err != nil {
		return fmt.Errorf("go test plugin %s: %w\n%s", resolvedPluginDir, err, string(output))
	}

	return nil
}

func runGoCommand(dir string, args ...string) ([]byte, error) {
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	return command.CombinedOutput()
}

func updateGoWorkUseEntry(workspaceRoot, newEntry string) error {
	goWorkPath := filepath.Join(workspaceRoot, "go.work")
	rawGoWork, err := os.ReadFile(goWorkPath)
	if err != nil {
		return fmt.Errorf("read go.work: %w", err)
	}

	lines := strings.Split(strings.ReplaceAll(string(rawGoWork), "\r\n", "\n"), "\n")
	useStart := -1
	useEnd := -1
	for index, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "use (" {
			useStart = index
			continue
		}
		if useStart >= 0 && trimmedLine == ")" {
			useEnd = index
			break
		}
	}

	if useStart < 0 || useEnd < 0 || useEnd <= useStart {
		return fmt.Errorf("go.work missing use block")
	}

	entries := make([]string, 0, useEnd-useStart)
	for _, line := range lines[useStart+1 : useEnd] {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		entries = append(entries, normalizeGoWorkEntry(trimmedLine))
	}

	newEntry = normalizeGoWorkEntry(newEntry)
	if newEntry == "" {
		return fmt.Errorf("go.work entry is required")
	}
	if !containsString(entries, newEntry) {
		entries = append(entries, newEntry)
	}
	sort.Strings(entries)

	updatedLines := append([]string{}, lines[:useStart+1]...)
	for _, entry := range entries {
		updatedLines = append(updatedLines, "\t"+entry)
	}
	updatedLines = append(updatedLines, lines[useEnd:]...)

	updatedGoWork := strings.Join(updatedLines, "\n")
	if !strings.HasSuffix(updatedGoWork, "\n") {
		updatedGoWork += "\n"
	}

	if err := os.WriteFile(goWorkPath, []byte(updatedGoWork), 0o644); err != nil {
		return fmt.Errorf("write go.work: %w", err)
	}

	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeGoWorkEntry(entry string) string {
	normalizedEntry := filepath.ToSlash(strings.TrimSpace(entry))
	if normalizedEntry == "" {
		return ""
	}
	if strings.HasPrefix(normalizedEntry, "./") || strings.HasPrefix(normalizedEntry, "../") {
		return normalizedEntry
	}
	if strings.HasPrefix(normalizedEntry, "/") {
		return normalizedEntry
	}
	return "./" + normalizedEntry
}

func toPascalCase(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	if len(parts) == 0 {
		return "Plugin"
	}

	var builder strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		builder.WriteString(strings.ToUpper(part[:1]))
		builder.WriteString(part[1:])
	}
	if builder.Len() == 0 {
		return "Plugin"
	}
	return builder.String()
}

func toPackageName(pluginID string) string {
	return strings.ReplaceAll(pluginID, "-", "")
}

func copyDir(sourceDir, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", targetDir, err)
	}

	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}

		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("build relative path for %s: %w", path, err)
		}
		if shouldSkipScaffoldCopy(relativePath, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		targetPath := filepath.Join(targetDir, relativePath)
		if entry.IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", targetPath, err)
			}
			return nil
		}

		return copyFile(path, targetPath)
	})
}

func shouldSkipScaffoldCopy(relativePath string, entry fs.DirEntry) bool {
	normalizedPath := filepath.ToSlash(strings.TrimSpace(relativePath))
	if normalizedPath == "" {
		return false
	}
	baseName := filepath.Base(normalizedPath)
	if entry.IsDir() && baseName == "dist" {
		return true
	}
	if strings.Contains(normalizedPath, "/dist/") {
		return true
	}
	return false
}

func copyFile(sourcePath, targetPath string) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", targetPath, err)
	}

	targetFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", targetPath, err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy %s to %s: %w", sourcePath, targetPath, err)
	}

	return nil
}
