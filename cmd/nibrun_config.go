// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"os"
	"strings"
)

const (
	nibrunAdminUser   = "gitea-admin"
	nibrunDefaultPort = "3000"
)

type nibrunConfigValues struct {
	Domain        string
	RootURL       string
	Port          string
	DataDir       string
	WorkDir       string
	AppDataPath   string
	Repositories  string
	GitHome       string
	HooksDir      string
	LogDir        string
	SecretKey     string
	InternalToken string
}

// Every path is absolute because the working directory is the read-only image
// holding the binary, and Gitea otherwise derives its work path from there.
//
// core.hooksPath is what makes this run at all: Gitea writes its delegate hooks
// as "#!/usr/bin/env bash" scripts, and the guest has no shell to run them. The
// setting sends git to a directory of symlinks back to this binary instead, and
// the argv[0] dispatch in package main turns each one into "gitea hook <name>".
const nibrunConfigTemplate = `APP_NAME = Gitea
RUN_MODE = prod
WORK_PATH = {{WORK_DIR}}

[server]
PROTOCOL = http
DOMAIN = {{DOMAIN}}
ROOT_URL = {{ROOT_URL}}
HTTP_ADDR = 0.0.0.0
HTTP_PORT = {{PORT}}
LOCAL_ROOT_URL = http://127.0.0.1:{{PORT}}/
APP_DATA_PATH = {{APP_DATA_PATH}}
DISABLE_SSH = true
START_SSH_SERVER = false
OFFLINE_MODE = true

[database]
DB_TYPE = sqlite3
PATH = {{APP_DATA_PATH}}/gitea.db

[repository]
ROOT = {{REPOSITORIES}}
DEFAULT_BRANCH = main

[git]
HOME_PATH = {{GIT_HOME}}

[git.config]
core.hooksPath = {{HOOKS_DIR}}

[security]
INSTALL_LOCK = true
SECRET_KEY = {{SECRET_KEY}}
INTERNAL_TOKEN = {{INTERNAL_TOKEN}}

[service]
DISABLE_REGISTRATION = true
REQUIRE_SIGNIN_VIEW = false

[session]
PROVIDER = file
PROVIDER_CONFIG = {{APP_DATA_PATH}}/sessions

[indexer]
ISSUE_INDEXER_TYPE = db
REPO_INDEXER_ENABLED = false

[mailer]
ENABLED = false

[log]
MODE = console
LEVEL = info
ROOT_PATH = {{LOG_DIR}}
`

func renderNibrunConfig(values nibrunConfigValues) string {
	return strings.NewReplacer(
		"{{DOMAIN}}", values.Domain,
		"{{ROOT_URL}}", values.RootURL,
		"{{PORT}}", values.Port,
		"{{WORK_DIR}}", values.WorkDir,
		"{{APP_DATA_PATH}}", values.AppDataPath,
		"{{REPOSITORIES}}", values.Repositories,
		"{{GIT_HOME}}", values.GitHome,
		"{{HOOKS_DIR}}", values.HooksDir,
		"{{LOG_DIR}}", values.LogDir,
		"{{SECRET_KEY}}", values.SecretKey,
		"{{INTERNAL_TOKEN}}", values.InternalToken,
	).Replace(nibrunConfigTemplate)
}

func nibrunPort() string {
	if value := os.Getenv("NIBRUN_HTTP_PORT"); value != "" {
		return value
	}
	return nibrunDefaultPort
}

func nibrunHostname() string {
	if value := os.Getenv("NIBRUN_HOSTNAME"); value != "" {
		return value
	}
	return "localhost"
}

// nibrun terminates TLS at its edge and forwards plain HTTP, so the public URL
// uses a scheme the server itself never speaks.
func nibrunRootURL() string {
	if value := os.Getenv("NIBRUN_HOSTNAME"); value != "" {
		return "https://" + value + "/"
	}
	return "http://localhost:" + nibrunPort() + "/"
}
