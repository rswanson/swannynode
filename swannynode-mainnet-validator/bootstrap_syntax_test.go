package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The bootstrap is assembled by string concatenation and contains nested
// heredocs: an outer `sudo bash -s <<'BOOTSTRAP'` wrapper, and inside it
// `<<'DROPIN'` blocks that write the systemd drop-ins. A stray delimiter,
// an unterminated heredoc, or an unbalanced quote produces a script that
// looks fine in a string-contains test and only fails when it runs against a
// real host — which, on the instance-store path, is in the middle of a
// validator migration. Parse both layers instead.
func TestBootstrapScriptIsValidBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	for name, cfg := range map[string]StackConfig{
		"instance-store": testCfg(),
		"ebs":            ebsCfg(),
	} {
		t.Run(name, func(t *testing.T) {
			script := bootstrapFor(t, cfg)
			requireParses(t, script, "outer bootstrap script")

			// bash -n treats the outer heredoc body as opaque data, so the
			// inner script (where the DROPIN heredocs live) needs its own pass.
			const open, close = "<<'BOOTSTRAP'\n", "\nBOOTSTRAP\n"
			_, after, found := strings.Cut(script, open)
			require.True(t, found, "bootstrap wrapper heredoc not found")
			inner, _, found := strings.Cut(after, close)
			require.True(t, found, "bootstrap wrapper heredoc is not terminated")
			requireParses(t, inner, "inner BOOTSTRAP heredoc body")
		})
	}
}

func requireParses(t *testing.T, script, what string) {
	t.Helper()
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s is not valid bash:\n%s", what, out)
	// Exit code alone is not enough. An unterminated heredoc — the most likely
	// way this generated script breaks — is only a WARNING to bash, which still
	// exits 0. A clean parse produces no output at all, so treat any output as
	// failure.
	require.Empty(t, string(out), "%s parsed with warnings (unterminated heredoc?):\n%s", what, out)
}
