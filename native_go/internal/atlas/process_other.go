//go:build !windows

package atlas

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
