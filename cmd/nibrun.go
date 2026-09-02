// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"gitea.dev/modules/generate"

	"github.com/urfave/cli/v3"
)

// The static git built by `make nibrun-git`. It is a build artifact and is not
// committed, so a plain `go build` produces a binary that reports the missing
// asset instead of failing to compile.
//
//go:embed nibrun_assets
var nibrunAssets embed.FS

const nibrunGitAsset = "nibrun_assets/git.gz"

// nibrunHooks are the server-side hooks Gitea delegates to itself. They are the
// subcommand names of "gitea hook", which is what the argv[0] dispatch relies on.
var nibrunHooks = []string{"pre-receive", "update", "post-receive", "proc-receive"}

func newNibrunCommand() *cli.Command {
	return &cli.Command{
		Name:  "nibrun",
		Usage: "Prepare the data volume and start the web server on nibrun",
		Description: "nibrun boots a single uploaded binary inside a microVM whose root " +
			"filesystem is read-only and carries no git and no shell. This command supplies " +
			"both from inside the binary, writes a configuration addressing the one writable " +
			"directory it is given, and then serves.",
		Action: runNibrun,
	}
}

func runNibrun(_ context.Context, _ *cli.Command) error {
	layout := newNibrunLayout(nibrunDataDir())

	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"create directories", layout.create},
		{"install git", layout.installGit},
		{"install hooks", layout.installHooks},
		{"write configuration", layout.writeConfig},
		{"export environment", layout.exportEnvironment},
		{"ensure administrator", layout.ensureAdmin},
	} {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	return nibrunServe(executable, layout.configPath())
}

type nibrunLayout struct{ dataDir string }

func newNibrunLayout(dataDir string) *nibrunLayout { return &nibrunLayout{dataDir: dataDir} }

func (l *nibrunLayout) binDir() string      { return filepath.Join(l.dataDir, "bin") }
func (l *nibrunLayout) hooksDir() string    { return filepath.Join(l.dataDir, "hooks") }
func (l *nibrunLayout) workDir() string     { return filepath.Join(l.dataDir, "gitea") }
func (l *nibrunLayout) customDir() string   { return filepath.Join(l.workDir(), "custom") }
func (l *nibrunLayout) appDataPath() string { return filepath.Join(l.workDir(), "data") }
func (l *nibrunLayout) logDir() string      { return filepath.Join(l.workDir(), "log") }
func (l *nibrunLayout) gitHome() string     { return filepath.Join(l.dataDir, "home") }
func (l *nibrunLayout) gitPath() string     { return filepath.Join(l.binDir(), "git") }

func (l *nibrunLayout) repositories() string { return filepath.Join(l.dataDir, "repositories") }

func (l *nibrunLayout) configPath() string {
	return filepath.Join(l.customDir(), "conf", "app.ini")
}

func (l *nibrunLayout) adminMarker() string {
	return filepath.Join(l.dataDir, ".admin-created")
}

func (l *nibrunLayout) create() error {
	for _, dir := range []string{
		l.binDir(),
		l.hooksDir(),
		filepath.Join(l.customDir(), "conf"),
		l.appDataPath(),
		l.logDir(),
		l.gitHome(),
		l.repositories(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	return nil
}

func (l *nibrunLayout) installGit() error {
	compressed, err := nibrunAssets.Open(nibrunGitAsset)
	if err != nil {
		return fmt.Errorf("%s is missing, build it with `make nibrun-git`", nibrunGitAsset)
	}
	defer func() { _ = compressed.Close() }()

	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("read asset: %w", err)
	}
	defer func() { _ = reader.Close() }()

	return writeNibrunExecutable(l.gitPath(), reader)
}

func (l *nibrunLayout) installHooks() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	for _, hook := range nibrunHooks {
		path := filepath.Join(l.hooksDir(), hook)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace %q: %w", hook, err)
		}
		if err := os.Symlink(executable, path); err != nil {
			return fmt.Errorf("link %q: %w", hook, err)
		}
	}
	return nil
}

func (l *nibrunLayout) writeConfig() error {
	if _, err := os.Stat(l.configPath()); err == nil {
		return nil
	}
	secret, err := generate.NewSecretKey()
	if err != nil {
		return fmt.Errorf("generate secret key: %w", err)
	}
	token, err := generate.NewInternalToken()
	if err != nil {
		return fmt.Errorf("generate internal token: %w", err)
	}
	body := renderNibrunConfig(nibrunConfigValues{
		Domain:        nibrunHostname(),
		RootURL:       nibrunRootURL(),
		Port:          nibrunPort(),
		DataDir:       l.dataDir,
		WorkDir:       l.workDir(),
		AppDataPath:   l.appDataPath(),
		Repositories:  l.repositories(),
		GitHome:       l.gitHome(),
		HooksDir:      l.hooksDir(),
		LogDir:        l.logDir(),
		SecretKey:     secret,
		InternalToken: token,
	})
	if err := os.WriteFile(l.configPath(), []byte(body), 0o600); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (l *nibrunLayout) exportEnvironment() error {
	for name, value := range map[string]string{
		"PATH": l.binDir() + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME": l.gitHome(),

		// The bundled git is built with the Makefile's default prefix of $HOME, so
		// its compiled-in system paths point at the build user's home directory and
		// every invocation warns about a directory the run user cannot read.
		"GIT_ATTR_NOSYSTEM": "1",

		// The hooks re-enter this binary with no arguments beyond the hook name, so
		// they find the configuration the same way the server did.
		"GITEA_WORK_DIR": l.workDir(),
		"GITEA_CUSTOM":   l.customDir(),
	} {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %q: %w", name, err)
		}
	}
	return nil
}

// The first user has to come from the command line: the install page is locked
// and self-registration is disabled. Both steps run as child processes so that
// serving starts from a single clean initialization.
func (l *nibrunLayout) ensureAdmin() error {
	if _, err := os.Stat(l.adminMarker()); err == nil {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	password, err := randomNibrunHex(9)
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}

	// "admin user create" opens the database without migrating it, so on a first
	// boot the tables it writes to do not exist yet.
	if err := l.run(executable, "migrate"); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := l.run(executable, "admin", "user", "create",
		"--username", nibrunAdminUser,
		"--password", password,
		"--email", nibrunAdminUser+"@"+nibrunHostname(),
		"--admin",
		"--must-change-password=false",
	); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	if err := os.WriteFile(l.adminMarker(), []byte(nibrunAdminUser), 0o644); err != nil {
		return fmt.Errorf("record marker: %w", err)
	}
	// Printed rather than logged because the log manager is only configured once
	// serving starts, and these credentials are the only way into a new instance.
	_, _ = fmt.Fprintf(os.Stdout, "Administrator %q created with initial password %q\n", nibrunAdminUser, password)
	return nil
}

func (l *nibrunLayout) run(executable string, args ...string) error {
	command := exec.Command(executable, append(args, "--config", l.configPath())...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func writeNibrunExecutable(path string, source io.Reader) error {
	staging := path + ".staging"
	file, err := os.Create(staging)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	if _, err := io.Copy(file, source); err != nil {
		_ = file.Close()
		return fmt.Errorf("copy contents: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("set mode: %w", err)
	}
	if err := os.Rename(staging, path); err != nil {
		return fmt.Errorf("move into place: %w", err)
	}
	return nil
}

func nibrunDataDir() string {
	if dir := os.Getenv("NIBRUN_DATA_DIR"); dir != "" {
		return dir
	}
	return "data"
}

func randomNibrunHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// NibrunHookArgs rewrites the arguments of a binary invoked through one of the
// hook symlinks into the equivalent "gitea hook <name>" invocation.
func NibrunHookArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	name := filepath.Base(args[0])
	for _, hook := range nibrunHooks {
		if name == hook {
			return append([]string{args[0], "hook", hook}, args[1:]...)
		}
	}
	return args
}
