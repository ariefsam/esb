package generator

import (
	"fmt"
	"regexp"
)

// snakeNameRE matches snake_case identifiers: a lowercase letter followed by
// lowercase letters/digits, with single underscores between segments. Used for
// aggregate, handler, projection, query, and field names.
var snakeNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// pascalNameRE matches PascalCase identifiers: an uppercase letter followed by
// letters/digits. Used for event names.
var pascalNameRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// validateSnakeName reports an error if name is not a valid snake_case
// identifier. kind is used in the error message (e.g. "aggregate name").
func validateSnakeName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if !snakeNameRE.MatchString(name) {
		return fmt.Errorf("invalid %s %q — use snake_case (lowercase letters, digits, single underscores; must start with a letter), e.g. bank_account", kind, name)
	}
	return nil
}

// validatePascalName reports an error if name is not a valid PascalCase
// identifier. kind is used in the error message (e.g. "event name").
func validatePascalName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if !pascalNameRE.MatchString(name) {
		return fmt.Errorf("invalid %s %q — use PascalCase (start with an uppercase letter, letters and digits only), e.g. OrderPlaced", kind, name)
	}
	return nil
}
