# Gitea on nibrun

[nibrun](https://nibrun.com) boots one uploaded binary inside a microVM. The
guest gets a writable volume and little else: the root filesystem is read-only
and carries a libc, and there is no shell, no package manager and no `git`.

Gitea needs `git` on the `PATH`, and it writes its delegate hooks as
`#!/usr/bin/env bash` scripts. Neither exists in the guest, so the `nibrun`
subcommand supplies both from inside the binary before it starts the server.

## Build

```sh
make nibrun
```

This produces `gitea-nibrun-linux-amd64`, a static `linux/amd64` binary that
carries the web assets and a statically linked git. It runs `make nibrun-git`
first, which builds that git in a container and writes it to
`cmd/nibrun_assets/git.gz`, where `cmd/nibrun.go` embeds it.

## Run

```sh
gitea-nibrun-linux-amd64 nibrun
```

The subcommand reads `NIBRUN_DATA_DIR`, `NIBRUN_HTTP_PORT` and
`NIBRUN_HOSTNAME` from the environment the host provides, lays the volume out
under the first of them, and writes an `app.ini` addressing it. It creates an
administrator on first boot and prints the generated password to stdout, which
is the only time that password is recoverable.

## How the hooks work

`app.ini` sets `core.hooksPath` through the `[git.config]` section, which Gitea
applies to its internal git configuration. That path holds one symlink per
server-side hook, each pointing back at the Gitea binary, and `NibrunHookArgs`
turns an invocation arriving through one of them into the matching
`gitea hook <name>` command.

The bash hooks Gitea writes into each repository are left alone. They are never
reached, because `core.hooksPath` takes precedence over a repository's own
`hooks` directory.

## Constraints worth knowing

- The artifact may be at most 256 MiB, and the guest is given 256 MiB of memory.
  Gitea settles around 120 MiB but peaks near 190 MiB while it migrates the
  database on first boot.
- `ISSUE_INDEXER_TYPE` is set to `db` to keep bleve out of that budget.
- SSH is disabled. The host publishes one HTTP port, so git runs over HTTPS.
- `admin user create` opens the database without migrating it, so first boot
  runs `migrate` before it.
