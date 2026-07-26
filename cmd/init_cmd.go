package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var initHere bool

var initCmd = &cobra.Command{
	Use:   "init <module-name>",
	Short: "Initialise a new event sourcing project",
	Long: `Creates a fully-structured Go project, either in a new directory named
after the last segment of the module path, or directly in the current
directory. When run interactively without --here, you'll be asked which
one you want.

Examples:
  esb init toko-online
  esb init github.com/myorg/toko-online
  esb init --here toko-online   # scaffold into the current directory`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		moduleName := args[0]

		// Derive directory name from module path last segment.
		parts := strings.Split(moduleName, "/")
		dirName := parts[len(parts)-1]

		useHere := initHere
		if !useHere && isInteractive() {
			var err error
			useHere, err = askUseExistingFolder(dirName)
			if err != nil {
				return fmt.Errorf("read prompt: %w", err)
			}
		}

		var destDir string
		if useHere {
			var err error
			destDir, err = filepath.Abs(".")
			if err != nil {
				return err
			}

			empty, err := dirIsEmpty(destDir)
			if err != nil {
				return fmt.Errorf("check directory %q: %w", destDir, err)
			}
			if !empty {
				fmt.Printf("Warning: %s is not empty; files with matching names will be overwritten.\n", destDir)
				if isInteractive() {
					ok, err := confirm("Continue? [y/N]: ")
					if err != nil {
						return fmt.Errorf("read prompt: %w", err)
					}
					if !ok {
						return fmt.Errorf("aborted")
					}
				}
			}
		} else {
			var err error
			destDir, err = filepath.Abs(dirName)
			if err != nil {
				return err
			}

			if _, err := os.Stat(destDir); err == nil {
				return fmt.Errorf("directory %q already exists", dirName)
			}

			if err := os.MkdirAll(destDir, 0755); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}
		}

		fmt.Printf("Initialising project %s in %s\n\n", moduleName, destDir)
		if err := generator.InitProject(moduleName, destDir); err != nil {
			return err
		}

		var cdStep string
		if !useHere {
			cdStep = fmt.Sprintf("  cd %s\n", dirName)
		}

		fmt.Printf(`
Done! Next steps:

%s  cp .env.example .env
  # Default mode is "embedded" — app jalan tanpa server ESB hidup.
  # Untuk pindah ke remote nanti: edit .env (EVENT_STORE_MODE=esb-server,
  # set ESB_URL/TENANT_ID/PROJECT_ID) lalu 'make migrate-to-esb'.
  make keygen   # kalau mau pakai mode esb-server

  esb add aggregate <name>
`, cdStep)
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initHere, "here", false, "scaffold directly into the current directory instead of prompting")
}

// isInteractive reports whether stdin is an interactive terminal.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// askUseExistingFolder prompts the user to choose between creating a new
// folder and scaffolding into the current directory.
func askUseExistingFolder(dirName string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Where do you want to initialise the project?")
	fmt.Printf("  1) Create a new folder (./%s)\n", dirName)
	fmt.Println("  2) Use the current folder")
	for {
		fmt.Print("Choose [1/2] (default 1): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(line) {
		case "", "1":
			return false, nil
		case "2":
			return true, nil
		default:
			fmt.Println("Please enter 1 or 2.")
		}
	}
}

// confirm asks a yes/no question and reports whether the user answered yes.
func confirm(prompt string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// dirIsEmpty reports whether the directory at path has no entries.
func dirIsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}
