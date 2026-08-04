//go:build darwin

package netiso

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"emperror.dev/errors"
)

// AnchorName is the pf anchor Wings owns. Rules live in an anchor rather than
// in the main ruleset so that loading them cannot disturb the rules macOS
// installs for itself -- Internet Sharing, the application firewall and
// AirDrop all live under com.apple, and replacing the main ruleset would
// discard them.
const AnchorName = "wings"

// AnchorPath is where the generated ruleset is written. pf reads the rules
// from the kernel once loaded, so this file exists to be reloadable and to be
// inspectable by an operator wondering what Wings did.
const AnchorPath = "/etc/pf.anchors/com.pterodactyl.wings"

var (
	mu    sync.Mutex
	token string // reference-count token from `pfctl -E`, empty when we hold none
)

// ErrAnchorNotReferenced is returned when the anchor exists but nothing in the
// main ruleset evaluates it, which would leave the rules loaded and inert.
var ErrAnchorNotReferenced = errors.Sentinel(
	`netiso: pf is not configured to evaluate wings' rules. Add these two lines to /etc/pf.conf:
    anchor "wings"
    load anchor "wings" from "/etc/pf.anchors/com.pterodactyl.wings"`)

// Supported reports whether this platform can enforce network isolation.
func Supported() bool {
	_, err := exec.LookPath("pfctl")
	return err == nil
}

// Available reports whether isolation can actually be applied right now.
// pfctl requires root, the same privilege per-server accounts already need.
func Available() bool {
	return Supported() && os.Geteuid() == 0
}

// Apply replaces the anchor's contents with the policy for the given servers
// and makes sure pf is running.
//
// Passing an empty slice clears the policy, which is the correct behaviour
// when the last server on a node is deleted.
func Apply(rules []Rule) error {
	body, err := Ruleset(rules)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	if !Available() {
		return errors.New("netiso: network isolation requires wings to run as root on darwin")
	}

	if err := writeAnchorFile(body); err != nil {
		return err
	}

	// Enable before loading. pf accepts rules while disabled, but a caller that
	// saw Apply succeed would reasonably assume they were in force.
	if err := enable(); err != nil {
		return err
	}

	if err := run("pfctl", "-a", AnchorName, "-f", AnchorPath); err != nil {
		return errors.WithMessage(err, "netiso: failed to load the wings anchor")
	}

	// Loading into an unreferenced anchor succeeds and enforces nothing, which
	// is the one failure mode here that looks exactly like success.
	referenced, err := anchorIsReferenced()
	if err != nil {
		return err
	}
	if !referenced {
		// macOS does not load /etc/pf.conf at boot unless pf was already
		// enabled, so on a freshly booted machine the kernel ruleset is empty
		// and references nothing, even though the file on disk is correct.
		// Enabling pf does not load it either. Recover from that rather than
		// refusing to start every time the machine reboots.
		loaded, lerr := loadMainRulesetIfConfigured()
		if lerr != nil {
			return lerr
		}
		if !loaded {
			return ErrAnchorNotReferenced
		}
		if referenced, err = anchorIsReferenced(); err != nil {
			return err
		}
		if !referenced {
			return ErrAnchorNotReferenced
		}
	}
	return nil
}

// MainConfigPath is the system pf configuration, which is what has to
// reference our anchor for any of the generated rules to be evaluated.
const MainConfigPath = "/etc/pf.conf"

// loadMainRulesetIfConfigured loads the system ruleset when it is the thing
// that would make our anchor live, and reports whether it did.
//
// The check on the file's contents matters: it means this only ever reloads a
// configuration that was already set up to evaluate our rules. An operator who
// has deliberately loaded some other ruleset, one that does not mention wings,
// does not have theirs replaced out from under them; they get the error telling
// them what to add instead.
func loadMainRulesetIfConfigured() (bool, error) {
	body, err := os.ReadFile(MainConfigPath)
	if err != nil {
		// No system config to fall back on, so the caller's error stands.
		return false, nil
	}
	if !strings.Contains(string(body), `anchor "`+AnchorName+`"`) {
		return false, nil
	}
	if err := run("pfctl", "-f", MainConfigPath); err != nil {
		return false, errors.WithMessage(err, "netiso: failed to load "+MainConfigPath)
	}
	return true, nil
}

// Clear removes every rule Wings loaded and releases its claim on pf.
//
// pf itself is left running if anything else still wants it; the token from
// `pfctl -E` is a reference count, so releasing ours only actually disables pf
// when no other holder remains. That matters because Internet Sharing and some
// VPN clients enable pf too, and disabling it outright would break them.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()

	if !Available() {
		return nil
	}
	if err := run("pfctl", "-a", AnchorName, "-F", "rules"); err != nil {
		return errors.WithMessage(err, "netiso: failed to flush the wings anchor")
	}
	if token != "" {
		// A failure to release leaks a reference, which keeps pf on. That is
		// the safe direction to fail in, so it is not fatal.
		_ = run("pfctl", "-X", token)
		token = ""
	}
	return nil
}

// enable claims a reference on pf, remembering the token so Clear can release
// exactly the one we took.
func enable() error {
	if token != "" {
		return nil
	}
	out, err := output("pfctl", "-E")
	if err != nil {
		return errors.WithMessage(err, "netiso: failed to enable pf")
	}
	// pfctl prints "Token : <n>" to stderr, which output() folds in.
	for _, line := range strings.Split(out, "\n") {
		if _, v, ok := strings.Cut(line, "Token :"); ok {
			token = strings.TrimSpace(v)
			break
		}
	}
	return nil
}

// anchorIsReferenced reports whether the main ruleset evaluates our anchor.
func anchorIsReferenced() (bool, error) {
	out, err := output("pfctl", "-s", "rules")
	if err != nil {
		return false, errors.WithMessage(err, "netiso: failed to read the active pf ruleset")
	}
	return strings.Contains(out, `anchor "`+AnchorName+`"`), nil
}

// writeAnchorFile writes the ruleset atomically, so a reload that races with a
// write can never read a half-written policy.
func writeAnchorFile(body string) error {
	if err := os.MkdirAll(filepath.Dir(AnchorPath), 0o755); err != nil {
		return errors.WithMessage(err, "netiso: failed to create the pf anchor directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(AnchorPath), ".com.pterodactyl.wings.*")
	if err != nil {
		return errors.WithMessage(err, "netiso: failed to stage the pf anchor")
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return errors.WithMessage(err, "netiso: failed to write the pf anchor")
	}
	if err := tmp.Close(); err != nil {
		return errors.WithMessage(err, "netiso: failed to write the pf anchor")
	}
	// Readable so an operator can inspect the policy, writable only by root so
	// that a server cannot rewrite the rules that confine it.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return errors.WithMessage(err, "netiso: failed to set permissions on the pf anchor")
	}
	if err := os.Rename(tmp.Name(), AnchorPath); err != nil {
		return errors.WithMessage(err, "netiso: failed to install the pf anchor")
	}
	return nil
}

func run(name string, args ...string) error {
	_, err := output(name, args...)
	return err
}

// output runs a command and returns its combined output, folding stderr in
// because pfctl reports both the token and its parse errors there.
func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), errors.Wrapf(err, "netiso: %s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}
