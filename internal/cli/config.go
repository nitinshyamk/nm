package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		GroupID: groupConfig,
		Short:   "Show nm's configuration",
		Long:    "Prints the configuration file's location and contents, creating it with defaults if it does not exist.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			path, err := config.Path()
			if err != nil {
				return err
			}
			blob, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("encoding the configuration: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n", path, blob)
			return nil
		},
	}
	cmd.AddCommand(newConfigGetCmd())
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one setting, with ~ expanded for paths",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			value, err := settingValue(cfg, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
}

// settingValue looks a setting up by its JSON name. Paths come back expanded,
// so callers such as the install task can use them directly.
func settingValue(cfg config.Config, key string) (string, error) {
	switch key {
	case "projects_root":
		return cfg.Projects(), nil
	case "worktrees_root":
		return cfg.Worktrees(), nil
	case "tasks_root":
		return cfg.Tasks(), nil
	case "install_dir":
		return cfg.Install(), nil
	}

	blob, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding the configuration: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		return "", fmt.Errorf("encoding the configuration: %w", err)
	}
	value, ok := fields[key]
	if !ok {
		known := make([]string, 0, len(fields))
		for name := range fields {
			known = append(known, name)
		}
		return "", fmt.Errorf("no setting %q (known: %s)", key, strings.Join(sorted(known), ", "))
	}
	return fmt.Sprintf("%v", value), nil
}
