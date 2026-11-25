package sshtest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

// ShellExec returns an Options.ExecStreams that runs each command with a real
// shell, the way sshd runs it: `shell -c COMMAND`, with HOME set to home.
// Processes the command leaves running in the background (a detached runner)
// keep running after the session ends, as on a real host. An empty shell
// means /bin/sh.
func ShellExec(shell, home string) func(ctx context.Context, command string, stdout, stderr io.Writer) int {
	if shell == "" {
		shell = "/bin/sh"
	}
	return func(ctx context.Context, command string, stdout, stderr io.Writer) int {
		cmd := exec.CommandContext(ctx, shell, "-c", command) // #nosec G204 -- test server executing the test's own command
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		cmd.Dir = home
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &exit) && exit.ExitCode() >= 0:
			return exit.ExitCode()
		default:
			return 255
		}
	}
}
