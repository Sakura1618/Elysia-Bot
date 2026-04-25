package main

import (
	"flag"
	"fmt"
	"os"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "scaffold":
		return runScaffold(args[1:])
	case "manifest":
		return runManifest(args[1:])
	case "package":
		return runPackage(args[1:])
	case "smoke":
		return runSmoke(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unsupported plugin-dev command %q\n\n%s", args[0], usageText())
	}
}

func runScaffold(args []string) error {
	flags := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	workspaceRoot := flags.String("workspace", "", "workspace root containing go.work")
	pluginID := flags.String("id", "", "repo-local plugin id, e.g. plugin-sample")
	pluginName := flags.String("name", "", "display name written into Manifest()")
	if err := flags.Parse(args); err != nil {
		return err
	}

	targetDir, err := pluginsdk.ScaffoldRepoPlugin(pluginsdk.ScaffoldOptions{
		WorkspaceRoot: *workspaceRoot,
		PluginID:      *pluginID,
		PluginName:    *pluginName,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "scaffolded %s\n", targetDir)
	return nil
}

func runManifest(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("manifest requires a subcommand: write or check\n\n%s", usageText())
	}

	switch args[0] {
	case "write":
		flags := flag.NewFlagSet("manifest write", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		pluginPath := flags.String("plugin", ".", "plugin directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		manifestPath, err := pluginsdk.WriteGeneratedManifest(*pluginPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "wrote %s\n", manifestPath)
		return nil
	case "check":
		flags := flag.NewFlagSet("manifest check", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		pluginPath := flags.String("plugin", ".", "plugin directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		if err := pluginsdk.CheckGeneratedManifest(*pluginPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "manifest OK: %s\n", *pluginPath)
		return nil
	default:
		return fmt.Errorf("unsupported manifest subcommand %q\n\n%s", args[0], usageText())
	}
}

func runPackage(args []string) error {
	flags := flag.NewFlagSet("package", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	pluginPath := flags.String("plugin", ".", "plugin directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	distDir, err := pluginsdk.PackagePlugin(*pluginPath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "packaged %s\n", distDir)
	return nil
}

func runSmoke(args []string) error {
	flags := flag.NewFlagSet("smoke", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	pluginPath := flags.String("plugin", ".", "plugin directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	resolvedPluginPath := *pluginPath
	if flags.NArg() > 1 {
		return fmt.Errorf("smoke accepts at most one positional plugin directory\n\n%s", usageText())
	}
	if flags.NArg() == 1 {
		if resolvedPluginPath != "." {
			return fmt.Errorf("smoke accepts either -plugin <plugin-dir> or one positional plugin directory\n\n%s", usageText())
		}
		resolvedPluginPath = flags.Arg(0)
	}

	distDir, err := pluginsdk.SmokePlugin(resolvedPluginPath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "smoke OK: %s (dist: %s)\n", resolvedPluginPath, distDir)
	return nil
}

func usageError() error {
	return fmt.Errorf("%s", usageText())
}

func printUsage() {
	fmt.Fprint(os.Stdout, usageText())
}

func usageText() string {
	return "plugin-dev usage:\n  plugin-dev scaffold -id plugin-example [-name \"Plugin Example\"] [-workspace <repo-root>]\n  plugin-dev manifest write [-plugin <plugin-dir>]\n  plugin-dev manifest check [-plugin <plugin-dir>]\n  plugin-dev package [-plugin <plugin-dir>]\n  plugin-dev smoke [-plugin <plugin-dir>]\n"
}
