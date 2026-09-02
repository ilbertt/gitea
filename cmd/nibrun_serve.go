// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// Replacing the process leaves the server as the only process in the guest, so
// it receives the runtime's signals itself rather than through a supervisor that
// would have to forward them.
func nibrunServe(executable, configPath string) error {
	return syscall.Exec(executable, []string{executable, "web", "--config", configPath}, os.Environ())
}
