// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build windows

package cmd

import (
	"os"
	"os/exec"
)

func nibrunServe(executable, configPath string) error {
	command := exec.Command(executable, "web", "--config", configPath)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}
