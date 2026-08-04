package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateOrdersAllowAfterDeny(t *testing.T) {
	got, err := Generate(Profile{
		ServerDir: "/srv/pterodactyl/volumes/abc",
		WingsRoot: "/srv/pterodactyl",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	deny := strings.Index(got, `(deny file-read* file-write* (subpath "/srv/pterodactyl")`)
	allow := strings.Index(got, `(allow file-read* file-write* (subpath "/srv/pterodactyl/volumes/abc")`)
	if deny == -1 || allow == -1 {
		t.Fatalf("expected both a deny and an allow:\n%s", got)
	}
	// Later rules win in SBPL, so the server would lose access to its own
	// directory if these were emitted the other way round.
	if allow < deny {
		t.Errorf("allow must come after deny or the server cannot read its own files:\n%s", got)
	}
}

// Without metadata on the denied parents a server cannot chdir into its own
// directory, and the server fails to boot with an error that names neither the
// sandbox nor the cause.
func TestGenerateAllowsTraversalMetadata(t *testing.T) {
	got, err := Generate(Profile{ServerDir: "/srv/p/volumes/abc", WingsRoot: "/srv/p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(got, `(allow file-read-metadata (subpath "/srv/p")`) {
		t.Errorf("expected traversal metadata to be permitted:\n%s", got)
	}
}

func TestGenerateRejectsServerDirOutsideRoot(t *testing.T) {
	_, err := Generate(Profile{ServerDir: "/somewhere/else", WingsRoot: "/srv/pterodactyl"})
	if err == nil {
		t.Fatal("expected an error when the server directory is outside the wings root")
	}
}

func TestGenerateRejectsRelativePaths(t *testing.T) {
	if _, err := Generate(Profile{ServerDir: "relative/path"}); err == nil {
		t.Fatal("expected an error for a relative server directory")
	}
	if _, err := Generate(Profile{ServerDir: "/srv/p/v/a", WingsRoot: "/srv/p", Deny: []string{"rel"}}); err == nil {
		t.Fatal("expected an error for a relative deny path")
	}
}

// A deny that contains the server's own directory is silently undone by the
// allow that follows it, so the operator's intent could not be honoured.
func TestGenerateRejectsDenyContainingServerDir(t *testing.T) {
	_, err := Generate(Profile{
		ServerDir: "/srv/p/volumes/abc",
		WingsRoot: "/srv/p",
		Deny:      []string{"/srv/p/volumes"},
	})
	if err == nil {
		t.Fatal("expected an error for a deny path containing the server's own directory")
	}
}

// A quote in a path would close the SBPL string literal early. That does not
// merely break parsing; it changes which paths the rule covers.
func TestGenerateEscapesQuotes(t *testing.T) {
	got, err := Generate(Profile{ServerDir: `/srv/p/v/we"ird`, WingsRoot: "/srv/p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(got, `we\"ird`) {
		t.Errorf("expected the quote to be escaped:\n%s", got)
	}
}

func TestGenerateIncludesExtraDenies(t *testing.T) {
	got, err := Generate(Profile{
		ServerDir: "/srv/p/volumes/abc",
		WingsRoot: "/srv/p",
		Deny:      []string{"/Users/someone/.ssh"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(got, `(deny file-read* file-write* (subpath "/Users/someone/.ssh"))`) {
		t.Errorf("expected the extra deny:\n%s", got)
	}
}

// The unit tests above assert what the profile says. This asserts the kernel
// agrees, which is the failure they cannot catch: a profile that reads
// correctly but is rejected leaves a server that will not start, and one that
// parses but does not confine is worse.
func TestProfileIsEnforcedByTheKernel(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}

	root := t.TempDir()
	serverDir := filepath.Join(root, "volumes", "abc")
	sibling := filepath.Join(root, "volumes", "def")
	for _, d := range []string{serverDir, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	secret := filepath.Join(root, "config.yml")
	if err := os.WriteFile(secret, []byte("token: hunter2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	own := filepath.Join(serverDir, "server.properties")
	if err := os.WriteFile(own, []byte("motd=hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	siblingFile := filepath.Join(sibling, "secrets.yml")
	if err := os.WriteFile(siblingFile, []byte("password: hunter2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	body, err := Generate(Profile{ServerDir: serverDir, WingsRoot: root})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	profile := filepath.Join(t.TempDir(), "server.sb")
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	confined := func(args ...string) error {
		return exec.Command("sandbox-exec", append([]string{"-f", profile}, args...)...).Run()
	}

	// Everything the file bits alone would allow, since all three are 0644 and
	// owned by the user running the test.
	if err := confined("/bin/cat", own); err != nil {
		t.Errorf("server should be able to read its own files: %v", err)
	}
	if err := confined("/bin/sh", "-c", "cd "+serverDir+" && echo x > w && rm w"); err != nil {
		t.Errorf("server should be able to chdir and write in its own directory: %v", err)
	}
	if err := confined("/bin/cat", secret); err == nil {
		t.Error("server read wings' config.yml, which holds the node token")
	}
	if err := confined("/bin/cat", siblingFile); err == nil {
		t.Error("server read another server's files")
	}
	// The confinement is worthless if a child process escapes it.
	if err := confined("/bin/sh", "-c", "cat "+secret); err == nil {
		t.Error("a child process escaped the sandbox and read the node token")
	}
}
