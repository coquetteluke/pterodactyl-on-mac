package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/pterodactyl/wings/internal/consolepty"
)

// newConsoleSupervisorCommand builds the command wings re-executes itself as in
// order to put a pseudo-terminal in front of a server process.
//
// This is plumbing rather than something an operator runs: the native
// environment spawns it, wires the server's stdin FIFO to its stdin and the
// console log to its stdout, and then tracks it as the server's process group
// leader. See internal/consolepty for why the terminal has to be owned by a
// process other than wings.
func newConsoleSupervisorCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "console-supervisor -- command [argument...]",
		Short:  "Runs a server process behind a pseudo-terminal. Used internally by wings.",
		Hidden: true,

		// The command line being supervised carries flags of its own -- the -c
		// of a shell, the -p of sandbox-exec -- and none of them are meant for
		// wings. Turning parsing off hands them through untouched, and also
		// keeps the root command's --config and --debug out of the way, neither
		// of which means anything here: no configuration is read and nothing is
		// logged.
		DisableFlagParsing: true,

		Run: func(cmd *cobra.Command, args []string) {
			// Cobra keeps the separator when flag parsing is off.
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "console-supervisor: no command was given to run")
				os.Exit(2)
			}

			code, err := consolepty.Run(consolepty.Config{
				Args: args,
				In:   os.Stdin,
				Out:  os.Stdout,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "console-supervisor: %v\n", err)
				os.Exit(1)
			}
			os.Exit(code)
		},
	}
}
