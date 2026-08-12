package consolepty

import (
	"os"
	"strings"
	"testing"
)

// runScript supervises a shell script and returns its exit code along with
// everything that reached the console log.
func runScript(t *testing.T, script string, in *os.File) (int, string) {
	t.Helper()

	out, err := os.CreateTemp(t.TempDir(), "console-*.log")
	if err != nil {
		t.Fatalf("could not create the console log: %v", err)
	}
	defer out.Close()

	code, err := Run(Config{Args: []string{"/bin/sh", "-c", script}, In: in, Out: out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatalf("could not read the console log back: %v", err)
	}
	return code, string(b)
}

// The whole point of the supervisor: the process must believe it is talking to
// a terminal, because that is what it checks before printing in colour.
func TestRunGivesTheProcessATerminal(t *testing.T) {
	_, out := runScript(t, `test -t 1 && echo "stdout is a terminal"; test -t 0 && echo "stdin is a terminal"`, nil)

	if !strings.Contains(out, "stdout is a terminal") {
		t.Errorf("stdout was not a terminal, got %q", out)
	}
	if !strings.Contains(out, "stdin is a terminal") {
		t.Errorf("stdin was not a terminal, got %q", out)
	}
}

// Colour survives the trip through the terminal to the console log unmodified,
// which is what the Panel ends up rendering.
func TestRunPassesEscapeSequencesThrough(t *testing.T) {
	_, out := runScript(t, `printf '\033[31mred\033[0m\n'`, nil)

	if !strings.Contains(out, "\033[31mred\033[0m") {
		t.Errorf("expected the escape sequences to survive, got %q", out)
	}
}

// A terminal translates a newline on the way out, which is why the console tail
// trims a trailing carriage return from every line it reads.
func TestRunTranslatesNewlinesForTheTerminal(t *testing.T) {
	_, out := runScript(t, `printf 'first\nsecond\n'`, nil)

	if !strings.Contains(out, "first\r\n") {
		t.Errorf("expected terminal line endings, got %q", out)
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	code, _ := runScript(t, `exit 7`, nil)

	if code != 7 {
		t.Errorf("expected exit code 7, got %d", code)
	}
}

// A killed process is reported the way a shell would report it, so the number
// can go to the Panel unchanged.
func TestRunReportsSignalUsingShellConvention(t *testing.T) {
	code, _ := runScript(t, `kill -9 $$`, nil)

	if code != 137 {
		t.Errorf("expected 128+SIGKILL, got %d", code)
	}
}

// The supervisor catches the signals wings aims at the process group so that it
// outlives the server, but it must not do that by ignoring them: an ignored
// signal survives execve, and the server would inherit it and then sit there
// ignoring the Panel's stop button.
//
// A server that is still killable exits 143 here. One that inherited the
// supervisor's disposition would shrug the signal off and reach the echo.
func TestRunLeavesTheProcessKillable(t *testing.T) {
	code, out := runScript(t, `kill -TERM $$; sleep 5; echo "ignored the signal"`, nil)

	if strings.Contains(out, "ignored the signal") {
		t.Fatalf("the process inherited an ignored SIGTERM, got %q", out)
	}
	if code != 143 {
		t.Errorf("expected 128+SIGTERM, got %d", code)
	}
}

// Console input arrives on a FIFO and has to reach the process as keystrokes.
func TestRunRelaysInputToTheProcess(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("could not create the input pipe: %v", err)
	}
	defer r.Close()

	go func() {
		defer w.Close()
		_, _ = w.WriteString("say hello\n")
	}()

	_, out := runScript(t, `read line; echo "received:$line"`, r)

	if !strings.Contains(out, "received:say hello") {
		t.Errorf("input did not reach the process, got %q", out)
	}
}

// A server with nothing wired to its stdin still has to boot.
func TestRunWithoutInputStillRuns(t *testing.T) {
	code, out := runScript(t, `echo started`, nil)

	if code != 0 {
		t.Errorf("expected a clean exit, got %d", code)
	}
	if !strings.Contains(out, "started") {
		t.Errorf("expected the output to be captured, got %q", out)
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "console-*.log")
	if err != nil {
		t.Fatalf("could not create the console log: %v", err)
	}
	defer out.Close()

	if _, err := Run(Config{Out: out}); err == nil {
		t.Error("expected an error when there is no command to run")
	}
}

func TestRunRejectsAMissingOutput(t *testing.T) {
	if _, err := Run(Config{Args: []string{"/bin/sh", "-c", "true"}}); err == nil {
		t.Error("expected an error when there is nowhere to write output")
	}
}

// The fallback path exists so that a machine that cannot hand out a terminal
// still boots its servers, just without colour.
func TestRunWithoutTerminalStillCapturesOutput(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "console-*.log")
	if err != nil {
		t.Fatalf("could not create the console log: %v", err)
	}
	defer out.Close()

	code, err := runWithoutTerminal(Config{
		Args: []string{"/bin/sh", "-c", `echo plain; exit 3`},
		Out:  out,
	})
	if err != nil {
		t.Fatalf("runWithoutTerminal: %v", err)
	}
	if code != 3 {
		t.Errorf("expected exit code 3, got %d", code)
	}

	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatalf("could not read the console log back: %v", err)
	}
	if !strings.Contains(string(b), "plain") {
		t.Errorf("expected the output to be captured, got %q", b)
	}
}
