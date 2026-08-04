//go:build darwin

package netiso

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit tests assert what the generated text says. This one asserts that pf
// agrees it is a ruleset at all, which is the failure the others cannot catch:
// a policy that reads correctly but does not parse leaves a node with no rules
// loaded and no obvious sign of it.
//
// -n parses and does not load, so this never touches the running ruleset.
func TestGeneratedRulesetParses(t *testing.T) {
	if _, err := exec.LookPath("pfctl"); err != nil {
		t.Skip("pfctl not available")
	}
	if os.Geteuid() != 0 {
		t.Skip("pfctl requires root even to parse, since it opens /dev/pf")
	}

	body, err := Ruleset([]Rule{
		{UUID: "a1b2c3d4", UID: 799, Ports: []int{25565, 25575}, AllowOut: []string{"192.168.1.50", "10.1.2.0/24"}},
		{UUID: "beefcafe", UID: 800, Ports: []int{25566}},
	})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}

	path := filepath.Join(t.TempDir(), "wings.pf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := exec.Command("pfctl", "-n", "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("pfctl rejected the generated ruleset: %v\n%s\n--- ruleset ---\n%s", err, out, body)
	}
	// pfctl exits 0 on some malformed input while still reporting a syntax
	// error, so the output has to be checked too.
	if strings.Contains(string(out), "syntax error") || strings.Contains(string(out), "errors") {
		t.Fatalf("pfctl reported a problem with the generated ruleset:\n%s\n--- ruleset ---\n%s", out, body)
	}
}
