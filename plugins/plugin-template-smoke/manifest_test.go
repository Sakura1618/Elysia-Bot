package plugintemplatesmoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func TestTemplateManifestConstantsStayInSync(t *testing.T) {
	t.Parallel()

	plugin := New(&recordingReplyService{}, Config{})
	manifest := Manifest()
	staticManifest := readStaticManifest(t)

	if !reflect.DeepEqual(plugin.Definition().Manifest, manifest) {
		t.Fatalf("definition manifest = %+v, want %+v", plugin.Definition().Manifest, manifest)
	}
	if manifest.ID != TemplatePluginID {
		t.Fatalf("manifest id = %q, want %q", manifest.ID, TemplatePluginID)
	}
	if manifest.SchemaVersion != pluginsdk.SupportedPluginManifestSchemaVersion {
		t.Fatalf("manifest schemaVersion = %q, want %q", manifest.SchemaVersion, pluginsdk.SupportedPluginManifestSchemaVersion)
	}
	if manifest.Name != TemplatePluginName {
		t.Fatalf("manifest name = %q, want %q", manifest.Name, TemplatePluginName)
	}
	if manifest.Entry.Module != TemplatePluginModule {
		t.Fatalf("manifest entry module = %q, want %q", manifest.Entry.Module, TemplatePluginModule)
	}
	if manifest.Entry.Symbol != TemplatePluginSymbol {
		t.Fatalf("manifest entry symbol = %q, want %q", manifest.Entry.Symbol, TemplatePluginSymbol)
	}
	if manifest.Mode != pluginsdk.ModeSubprocess {
		t.Fatalf("manifest mode = %q, want %q", manifest.Mode, pluginsdk.ModeSubprocess)
	}
	if len(manifest.Permissions) != 1 || manifest.Permissions[0] != "reply:send" {
		t.Fatalf("unexpected manifest permissions %+v", manifest.Permissions)
	}
	if manifest.ConfigSchema["type"] != "object" {
		t.Fatalf("unexpected config schema %+v", manifest.ConfigSchema)
	}
	if manifest.Publish == nil {
		t.Fatal("manifest publish metadata is required")
	}
	if manifest.Publish.SourceType != TemplatePluginPublishSourceType {
		t.Fatalf("manifest publish sourceType = %q, want %q", manifest.Publish.SourceType, TemplatePluginPublishSourceType)
	}
	if manifest.Publish.SourceURI != TemplatePluginPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, TemplatePluginPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != TemplatePluginRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, TemplatePluginRuntimeVersionRange)
	}

	goModulePath := readGoModulePath(t)
	if !strings.HasSuffix(goModulePath, "/"+manifest.Entry.Module) {
		t.Fatalf("go.mod module = %q, want suffix %q so it stays aligned with entry.module %q", goModulePath, "/"+manifest.Entry.Module, manifest.Entry.Module)
	}

	staticManifestBytes, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if err := assertTemplateManifestArtifactMatchesGeneratedTruth(staticManifestBytes, manifest); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(staticManifest, manifest) {
		t.Fatalf("manifest.json decoded payload = %+v, want %+v", staticManifest, manifest)
	}
}

func TestPluginTemplateSmokeManifestDriftDetected(t *testing.T) {
	t.Parallel()

	manifest := Manifest()
	staleManifest := readStaticManifest(t)
	staleManifest.Name = staleManifest.Name + " drifted"

	staleManifestBytes, err := marshalGeneratedManifest(staleManifest)
	if err != nil {
		t.Fatalf("marshal stale manifest: %v", err)
	}

	err = assertTemplateManifestArtifactMatchesGeneratedTruth(staleManifestBytes, manifest)
	if err == nil {
		t.Fatal("expected manifest drift to be detected")
	}
	if !strings.Contains(err.Error(), "manifest.json is out of date") {
		t.Fatalf("expected out-of-date manifest drift error, got %v", err)
	}
}

func readStaticManifest(t *testing.T) pluginsdk.PluginManifest {
	t.Helper()

	rawManifest, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}

	var manifest pluginsdk.PluginManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}

	return manifest
}

func marshalGeneratedManifest(manifest pluginsdk.PluginManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(rawManifest, '\n'), nil
}

func assertTemplateManifestArtifactMatchesGeneratedTruth(staticManifestBytes []byte, manifest pluginsdk.PluginManifest) error {
	generatedManifest, err := marshalGeneratedManifest(manifest)
	if err != nil {
		return fmt.Errorf("marshal generated manifest: %w", err)
	}
	if !bytes.Equal(normalizeLineEndings(staticManifestBytes), normalizeLineEndings(generatedManifest)) {
		return fmt.Errorf("manifest.json is out of date\n--- static ---\n%s\n--- generated ---\n%s", staticManifestBytes, generatedManifest)
	}
	return nil
}

func readGoModulePath(t *testing.T) string {
	t.Helper()

	rawGoMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	for line := range strings.SplitSeq(string(rawGoMod), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}

	t.Fatal("go.mod missing module declaration")
	return ""
}

func normalizeLineEndings(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}
