//go:build windows

package tool

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}
