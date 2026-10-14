//go:build !linux

package agent

import "os/exec"

func configureTestProcess(cmd *exec.Cmd) {}
