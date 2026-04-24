package pluginsdk

import (
	"strings"
	"testing"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

type stubEventHandler struct{}

func (stubEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	return nil
}

func TestPluginManifestValidateAcceptsMinimalContract(t *testing.T) {
	t.Parallel()

	manifest := PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-echo",
		Name:          "Echo Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          ModeSubprocess,
		Entry: PluginEntry{
			Module: "plugins/echo",
			Symbol: "Plugin",
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected manifest to validate, got %v", err)
	}
}

func TestPluginManifestValidateRejectsInvalidPermissionFormat(t *testing.T) {
	t.Parallel()

	manifest := PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-bad-perm",
		Name:          "Bad Permission Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          ModeSubprocess,
		Permissions:   []string{"reply-send"},
		Entry:         PluginEntry{Module: "plugins/bad", Symbol: "Plugin"},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid permission format to fail validation")
	}
}

func TestPluginManifestValidateRejectsInvalidManifest(t *testing.T) {
	t.Parallel()

	manifest := PluginManifest{}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid manifest to fail validation")
	}

	manifest = PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-bad",
		Name:          "Bad Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          "embedded",
		Entry:         PluginEntry{Module: "plugins/bad"},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected unsupported mode to fail validation")
	}
}

func TestPluginManifestValidateAcceptsPublishMetadata(t *testing.T) {
	t.Parallel()

	manifest := PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-published",
		Name:          "Published Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          ModeSubprocess,
		Publish: &PluginPublish{
			SourceType:          PublishSourceTypeGit,
			SourceURI:           "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo",
			RuntimeVersionRange: ">=0.1.0 <1.0.0",
		},
		Entry: PluginEntry{Module: "plugins/published", Symbol: "Plugin"},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected publish metadata to validate, got %v", err)
	}
}

func TestPluginManifestValidateRejectsInvalidPublishMetadata(t *testing.T) {
	t.Parallel()

	base := PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-published",
		Name:          "Published Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          ModeSubprocess,
		Publish: &PluginPublish{
			SourceType:          PublishSourceTypeGit,
			SourceURI:           "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo",
			RuntimeVersionRange: ">=0.1.0 <1.0.0",
		},
		Entry: PluginEntry{Module: "plugins/published", Symbol: "Plugin"},
	}

	tests := []struct {
		name       string
		mutate     func(*PluginManifest)
		wantErrMsg string
	}{
		{
			name: "missing source type",
			mutate: func(manifest *PluginManifest) {
				manifest.Publish.SourceType = ""
			},
			wantErrMsg: "sourceType is required",
		},
		{
			name: "unsupported source type",
			mutate: func(manifest *PluginManifest) {
				manifest.Publish.SourceType = "directory"
			},
			wantErrMsg: "unsupported sourceType",
		},
		{
			name: "invalid source uri",
			mutate: func(manifest *PluginManifest) {
				manifest.Publish.SourceURI = "plugins/plugin-echo"
			},
			wantErrMsg: "sourceUri",
		},
		{
			name: "invalid runtime version range",
			mutate: func(manifest *PluginManifest) {
				manifest.Publish.RuntimeVersionRange = "v0"
			},
			wantErrMsg: "runtimeVersionRange",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manifest := base
			publish := *base.Publish
			manifest.Publish = &publish
			tt.mutate(&manifest)

			err := manifest.Validate()
			if err == nil {
				t.Fatal("expected publish validation to fail")
			}
			if !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Fatalf("expected error to contain %q, got %v", tt.wantErrMsg, err)
			}
		})
	}
}

func TestRegistryRegistersAndListsPlugin(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	plugin := Plugin{
		Manifest: PluginManifest{
			SchemaVersion: SupportedPluginManifestSchemaVersion,
			ID:            "plugin-echo",
			Name:          "Echo Plugin",
			Version:       "0.1.0",
			APIVersion:    "v0",
			Mode:          ModeSubprocess,
			Entry: PluginEntry{
				Module: "plugins/echo",
				Symbol: "Plugin",
			},
		},
		Handlers: Handlers{Event: stubEventHandler{}},
	}

	if err := registry.Register(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	registered, err := registry.Get("plugin-echo")
	if err != nil {
		t.Fatalf("get plugin: %v", err)
	}
	if registered.Manifest.ID != "plugin-echo" {
		t.Fatalf("unexpected plugin id %q", registered.Manifest.ID)
	}

	manifests := registry.List()
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}

	if err := registry.Register(plugin); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestPluginManifestValidateRejectsMissingOrUnsupportedSchemaVersion(t *testing.T) {
	t.Parallel()

	base := PluginManifest{
		SchemaVersion: SupportedPluginManifestSchemaVersion,
		ID:            "plugin-schema",
		Name:          "Schema Plugin",
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          ModeSubprocess,
		Entry:         PluginEntry{Module: "plugins/schema", Symbol: "Plugin"},
	}

	tests := []struct {
		name       string
		mutate     func(*PluginManifest)
		wantErrMsg string
	}{
		{
			name: "missing schema version",
			mutate: func(manifest *PluginManifest) {
				manifest.SchemaVersion = ""
			},
			wantErrMsg: "schemaVersion is required",
		},
		{
			name: "unsupported schema version",
			mutate: func(manifest *PluginManifest) {
				manifest.SchemaVersion = "v99"
			},
			wantErrMsg: `unsupported schemaVersion "v99"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manifest := base
			tt.mutate(&manifest)

			err := manifest.Validate()
			if err == nil {
				t.Fatal("expected schema version validation to fail")
			}
			if !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Fatalf("expected error to contain %q, got %v", tt.wantErrMsg, err)
			}
		})
	}
}

func TestValidateRuntimeVersionRangeAndSatisfiesRange(t *testing.T) {
	t.Parallel()

	if err := ValidateRuntimeVersionRange(">=0.1.0 <1.0.0"); err != nil {
		t.Fatalf("expected runtimeVersionRange to validate, got %v", err)
	}

	for _, tc := range []struct {
		name       string
		current    string
		required   string
		want       bool
		wantErrMsg string
	}{
		{name: "satisfied with v prefix", current: "v0.1.0", required: ">=0.1.0 <1.0.0", want: true},
		{name: "not satisfied", current: "0.1.0", required: ">=0.2.0 <1.0.0", want: false},
		{name: "invalid range", current: "0.1.0", required: "v0", wantErrMsg: `runtimeVersionRange "v0" must use one or two comparator clauses`},
		{name: "invalid current version", current: "0.1", required: ">=0.1.0", wantErrMsg: `runtime version "0.1" must use major.minor.patch`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := RuntimeVersionSatisfiesRange(tc.current, tc.required)
			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatal("expected runtime version evaluation to fail")
				}
				if !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("expected error to contain %q, got %v", tc.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("evaluate runtime version range: %v", err)
			}
			if got != tc.want {
				t.Fatalf("RuntimeVersionSatisfiesRange(%q, %q) = %t, want %t", tc.current, tc.required, got, tc.want)
			}
		})
	}
}
