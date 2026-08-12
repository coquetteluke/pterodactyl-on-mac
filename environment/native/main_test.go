//go:build darwin

package native

import (
	"fmt"
	"os"
	"testing"

	"github.com/pterodactyl/wings/internal/consolepty"
)

// consoleSupervisorArg marks a re-execution of the test binary that should act
// as the console supervisor instead of running tests.
//
// The environment starts a server by re-executing the wings binary with a
// hidden subcommand, which under `go test` is not wings but the test binary.
// Left alone it would run the entire suite again, once per server started,
// until the machine ran out of processes.
const consoleSupervisorArg = "-native-console-supervisor"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == consoleSupervisorArg {
		code, err := consolepty.Run(consolepty.Config{
			Args: os.Args[2:],
			In:   os.Stdin,
			Out:  os.Stdout,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(code)
	}

	// Point the environment at the branch above. Servers started by the tests
	// then run behind a real pseudo-terminal, which is the arrangement being
	// tested: console output arriving through a terminal, signals reaching a
	// group with the supervisor at its head, and resources sampled across it.
	consoleSupervisorArgv = func() ([]string, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return []string{exe, consoleSupervisorArg}, nil
	}

	os.Exit(m.Run())
}
