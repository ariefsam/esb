package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var initCmd = &cobra.Command{
	Use:   "init <module-name>",
	Short: "Initialise a new event sourcing project",
	Long: `Creates a fully-structured Go project in a new directory named after the
last segment of the module path.

Examples:
  esb init toko-online
  esb init github.com/myorg/toko-online`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		moduleName := args[0]

		// Derive directory name from module path last segment.
		parts := strings.Split(moduleName, "/")
		dirName := parts[len(parts)-1]

		destDir, _ := filepath.Abs(dirName)

		if _, err := os.Stat(destDir); err == nil {
			return fmt.Errorf("directory %q already exists", dirName)
		}

		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}

		fmt.Printf("Initialising project %s\n\n", moduleName)
		if err := generator.InitProject(moduleName, destDir); err != nil {
			return err
		}

		fmt.Printf(`
Done! Next steps:

  cd %s
  cp .env.example .env
  # edit .env — set ESB_URL, TENANT_ID, PROJECT_ID, JWT_ISSUER
  make keygen
  # paste the printed PUBLIC_KEY into the ESB server's PUBLIC_KEYS env var

  esb add aggregate <name>
`, dirName)
		return nil
	},
}
