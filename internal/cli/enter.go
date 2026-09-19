package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/nitinshyamk/nm/internal/shellint"
)

// enterDir asks the shell wrapper to move to dir. Without the wrapper loaded
// there is no way to change the caller's directory, so nm prints the path and
// says once how to make it automatic.
func enterDir(out io.Writer, dir string) error {
	delivered, err := shellint.RequestCD(dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, dir)
	if !delivered {
		fmt.Fprintf(out, "\n%s\n", shellint.Hint(currentShell()))
	}
	return nil
}

// sorted returns the values in order, for stable error messages.
func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
