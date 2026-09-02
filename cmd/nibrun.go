// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gitea.dev/modules/generate"

	"github.com/urfave/cli/v3"
)

// The static git built by `make nibrun-git`. It is a build artifact and is not
// committed, so a plain `go build` reports the missing asset at run time instead
// of failing to compile.
//
//go:embed nibrun_assets
var nibrunAssets embed.FS

// The subcommand names of "gitea hook", which the argv[0] dispatch relies on.
var nibrunHooks = []string{"pre-receive", "update", "post-receive", "proc-receive"}

const nibrunAdminUser = "gitea-admin"

// newNibrunCommand serves exactly as "web" does, after laying out the single
// writable directory the host gives it. Preparing in Before rather than in the
// action is what lets it run in this process: the work path is resolved from the
// environment between the two.
func newNibrunCommand() *cli.Command {
	command := newWebCommand()
	command.Name = "nibrun"
	command.Usage = "Prepare the data volume and start the web server on nibrun"
	command.Description = "nibrun boots one uploaded binary inside a microVM whose root " +
		"filesystem is read-only and carries no git and no shell. This command supplies both " +
		"from inside the binary before serving."

	before := command.Before
	command.Before = func(ctx context.Context, c *cli.Command) (context.Context, error) {
		ctx, err := before(ctx, c)
		if err != nil {
			return ctx, err
		}
		return ctx, prepareNibrun()
	}
	return command
}

func prepareNibrun() error {
	dataDir := "data"
	if dir := os.Getenv("NIBRUN_DATA_DIR"); dir != "" {
		dataDir = dir
	}
	path := func(parts ...string) string {
		return filepath.Join(append([]string{dataDir}, parts...)...)
	}
	workDir, customDir := path("gitea"), path("gitea", "custom")
	configPath := filepath.Join(customDir, "conf", "app.ini")

	for _, dir := range []string{
		path("bin"), path("hooks"), path("home"), path("repositories"),
		filepath.Join(customDir, "conf"), filepath.Join(workDir, "data"), filepath.Join(workDir, "log"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	if err := installNibrunGit(path("bin", "git")); err != nil {
		return fmt.Errorf("install git: %w", err)
	}
	if err := installNibrunHooks(path("hooks")); err != nil {
		return fmt.Errorf("install hooks: %w", err)
	}
	if err := writeNibrunConfig(configPath, dataDir); err != nil {
		return fmt.Errorf("write configuration: %w", err)
	}
	for name, value := range map[string]string{
		"PATH": path("bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME": path("home"),

		// The bundled git is built with the Makefile's default prefix of $HOME, so its
		// compiled-in system paths point at a directory the run user cannot read.
		"GIT_ATTR_NOSYSTEM": "1",

		// Read back by the hooks, which re-enter this binary with only a hook name.
		"GITEA_WORK_DIR": workDir,
		"GITEA_CUSTOM":   customDir,
	} {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %q: %w", name, err)
		}
	}
	return ensureNibrunAdmin(path(".admin-created"), configPath)
}

func installNibrunGit(path string) error {
	compressed, err := nibrunAssets.Open("nibrun_assets/git.gz")
	if err != nil {
		return errors.New("git.gz is missing, build it with `make nibrun-git`")
	}
	defer func() { _ = compressed.Close() }()

	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	binary, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	return os.WriteFile(path, binary, 0o755)
}

// Gitea writes its delegate hooks as "#!/usr/bin/env bash" scripts, which the
// guest cannot execute. core.hooksPath sends git to these symlinks instead.
func installNibrunHooks(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	for _, hook := range nibrunHooks {
		path := filepath.Join(dir, hook)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(self, path); err != nil {
			return fmt.Errorf("link %q: %w", hook, err)
		}
	}
	return nil
}

// Every path is absolute because the working directory is the read-only image
// holding the binary, which is where Gitea would otherwise put all of these.
const nibrunConfigTemplate = `APP_NAME = Gitea
RUN_MODE = prod
WORK_PATH = $DATA/gitea

[server]
PROTOCOL = http
DOMAIN = $DOMAIN
ROOT_URL = $ROOT_URL
HTTP_ADDR = 0.0.0.0
HTTP_PORT = $PORT
APP_DATA_PATH = $DATA/gitea/data
DISABLE_SSH = true

[database]
DB_TYPE = sqlite3
PATH = $DATA/gitea/data/gitea.db

[repository]
ROOT = $DATA/repositories

[git]
HOME_PATH = $DATA/home

[git.config]
core.hooksPath = $DATA/hooks

[security]
INSTALL_LOCK = true
SECRET_KEY = $SECRET_KEY
INTERNAL_TOKEN = $INTERNAL_TOKEN

[service]
DISABLE_REGISTRATION = true

[indexer]
ISSUE_INDEXER_TYPE = db

[log]
MODE = console
ROOT_PATH = $DATA/gitea/log
`

func writeNibrunConfig(configPath, dataDir string) error {
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}
	secret, err := generate.NewSecretKey()
	if err != nil {
		return err
	}
	token, err := generate.NewInternalToken()
	if err != nil {
		return err
	}
	// nibrun terminates TLS at its edge and forwards plain HTTP, so the public URL
	// uses a scheme the server itself never speaks.
	rootURL := "http://localhost:" + nibrunPort() + "/"
	if os.Getenv("NIBRUN_HOSTNAME") != "" {
		rootURL = "https://" + nibrunHostname() + "/"
	}
	body := strings.NewReplacer(
		"$DATA", dataDir,
		"$DOMAIN", nibrunHostname(),
		"$ROOT_URL", rootURL,
		"$PORT", nibrunPort(),
		"$SECRET_KEY", secret,
		"$INTERNAL_TOKEN", token,
	).Replace(nibrunConfigTemplate)
	return os.WriteFile(configPath, []byte(body), 0o600)
}

func nibrunHostname() string {
	if host := os.Getenv("NIBRUN_HOSTNAME"); host != "" {
		return host
	}
	return "localhost"
}

func nibrunPort() string {
	if port := os.Getenv("NIBRUN_HTTP_PORT"); port != "" {
		return port
	}
	return "3000"
}

// The first user has to come from the command line: the install page is locked
// and self-registration is disabled. Both steps run as child processes so that
// serving starts from a single clean initialization.
func ensureNibrunAdmin(marker, configPath string) error {
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	run := func(args ...string) error {
		command := exec.Command(self, append(args, "--config", configPath)...)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		return command.Run()
	}
	// "admin user create" opens the database without migrating it, so on a first
	// boot the tables it writes to do not exist yet.
	if err := run("migrate"); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := run("admin", "user", "create",
		"--username", nibrunAdminUser,
		"--email", nibrunAdminUser+"@"+nibrunHostname(),
		"--admin", "--random-password", "--must-change-password=false",
	); err != nil {
		return fmt.Errorf("create administrator: %w", err)
	}
	return os.WriteFile(marker, nil, 0o644)
}

// NibrunHookArgs rewrites the arguments of a binary invoked through one of the
// hook symlinks into the equivalent "gitea hook <name>" invocation.
func NibrunHookArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	name := filepath.Base(args[0])
	if !slices.Contains(nibrunHooks, name) {
		return args
	}
	return append([]string{args[0], "hook", name}, args[1:]...)
}
