// Command quarshell-isolation-child is the restricted re-exec target. The
// supervisor may point LinuxOptions.Self at this binary or call
// RunChildIfRequested from its own main function.
package main

import (
	"fmt"
	"os"

	"github.com/KLORQ/quarshell-core/domain"
	"github.com/KLORQ/quarshell-driver/isolation"
)

func main() {
	handled, err := isolation.RunChildIfRequested()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolation child:", domain.SafeMessage(err))
		os.Exit(1)
	}
	if !handled {
		fmt.Fprintln(os.Stderr, "isolation child: not launched by a supervisor")
		os.Exit(2)
	}
}
