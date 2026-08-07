package main

import (
	"fmt"
	"strings"

	"github.com/shreyam1008/dbterm/internal/osservice"
)

const backupServiceUsage = "usage: dbterm backup service <install|uninstall|start|stop|restart|enable|disable|status> [--system|--user] [--run-as USER] [--config-dir PATH --state-dir PATH --log-dir PATH]"

type backupServiceRequest struct {
	Action         string
	Scope          osservice.Scope
	AllScopes      bool
	RunAsUser      string
	ConfigDir      string
	StateDir       string
	LogDir         string
	explicitConfig bool
	explicitState  bool
	explicitLog    bool
}

func (request backupServiceRequest) withScope(scope osservice.Scope) backupServiceRequest {
	request.Scope = scope
	request.AllScopes = false
	return request
}

func parseBackupServiceRequest(args []string) (backupServiceRequest, error) {
	request := backupServiceRequest{Scope: osservice.ScopeUser}
	var scopeSelected bool
	for index := 0; index < len(args); index++ {
		token := strings.TrimSpace(args[index])
		if token == "" {
			continue
		}
		if isHelpArg(token) {
			return backupServiceRequest{}, fmt.Errorf(backupServiceUsage)
		}
		switch token {
		case "--system":
			if scopeSelected && request.Scope != osservice.ScopeSystem {
				return backupServiceRequest{}, fmt.Errorf("choose only one service scope")
			}
			request.Scope, scopeSelected = osservice.ScopeSystem, true
			continue
		case "--user":
			if scopeSelected && request.Scope != osservice.ScopeUser {
				return backupServiceRequest{}, fmt.Errorf("choose only one service scope")
			}
			request.Scope, scopeSelected = osservice.ScopeUser, true
			continue
		case "--all":
			request.AllScopes = true
			continue
		}

		name, value, hasValue := strings.Cut(token, "=")
		switch name {
		case "--scope", "--run-as", "--config-dir", "--state-dir", "--log-dir":
			if !hasValue {
				index++
				if index >= len(args) {
					return backupServiceRequest{}, fmt.Errorf("%s requires a value", name)
				}
				value = args[index]
			}
			value = strings.TrimSpace(value)
			if value == "" {
				return backupServiceRequest{}, fmt.Errorf("%s requires a non-empty value", name)
			}
			switch name {
			case "--scope":
				parsed := osservice.Scope(strings.ToLower(value))
				if parsed != osservice.ScopeUser && parsed != osservice.ScopeSystem {
					return backupServiceRequest{}, fmt.Errorf("--scope must be user or system")
				}
				if scopeSelected && request.Scope != parsed {
					return backupServiceRequest{}, fmt.Errorf("choose only one service scope")
				}
				request.Scope, scopeSelected = parsed, true
			case "--run-as":
				request.RunAsUser = value
			case "--config-dir":
				request.ConfigDir, request.explicitConfig = value, true
			case "--state-dir":
				request.StateDir, request.explicitState = value, true
			case "--log-dir":
				request.LogDir, request.explicitLog = value, true
			}
			continue
		}
		if strings.HasPrefix(token, "-") {
			return backupServiceRequest{}, fmt.Errorf("unknown backup service option %q", token)
		}
		if request.Action != "" {
			return backupServiceRequest{}, fmt.Errorf("backup service accepts exactly one action")
		}
		request.Action = strings.ToLower(token)
	}

	validAction := false
	for _, action := range []string{"install", "uninstall", "start", "stop", "restart", "enable", "disable", "status"} {
		if request.Action == action {
			validAction = true
			break
		}
	}
	if !validAction {
		return backupServiceRequest{}, fmt.Errorf(backupServiceUsage)
	}
	if request.AllScopes && request.Action != "status" {
		return backupServiceRequest{}, fmt.Errorf("--all is only valid with backup service status")
	}
	if request.AllScopes && scopeSelected {
		return backupServiceRequest{}, fmt.Errorf("--all cannot be combined with --user, --system, or --scope")
	}
	if request.RunAsUser != "" && request.Scope != osservice.ScopeSystem {
		return backupServiceRequest{}, fmt.Errorf("--run-as is only valid with --system")
	}
	if request.Scope == osservice.ScopeSystem && request.Action == "install" && (!request.explicitConfig || !request.explicitState || !request.explicitLog) {
		return backupServiceRequest{}, fmt.Errorf("system installation must preserve the intended non-administrator data paths; pass explicit --config-dir, --state-dir, and --log-dir values")
	}
	return request, nil
}
