package osservice

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const (
	launchdLabel          = "io.github.shreyam1008.dbterm.backup"
	windowsTaskName       = `dbterm Backup Agent`
	windowsSystemTaskName = `dbterm Backup Agent (System)`
	windowsLocalSystemSID = "S-1-5-18"
	windowsTaskXMLNS      = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	windowsTaskXMLLevel   = "1.4"
)

func renderSystemdUnit(options Options) []byte {
	return renderSystemdDefinition(options, "", "default.target")
}

func renderSystemdSystemUnit(options Options, runAsUser string) []byte {
	return renderSystemdDefinition(options, runAsUser, "multi-user.target")
}

func renderSystemdDefinition(options Options, runAsUser, wantedBy string) []byte {
	standardOutput := "append:" + path.Join(options.LogDir, backupLogName)
	standardError := "append:" + path.Join(options.LogDir, backupErrorLogName)
	command := make([]string, 0, 9)
	for _, argument := range agentCommand(options) {
		command = append(command, systemdQuote(argument))
	}
	userDirective := ""
	if runAsUser != "" {
		userDirective = "User=" + runAsUser + "\n"
	}
	definition := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
%sType=simple
ExecStart=:%s
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
UMask=0077
StandardOutput=%s
StandardError=%s

[Install]
WantedBy=%s
`, serviceDescription, userDirective, strings.Join(command, " "), systemdQuote(standardOutput), systemdQuote(standardError), wantedBy)
	return []byte(definition)
}

func renderLaunchdPlist(options Options) []byte {
	return renderLaunchdDefinition(options, "")
}

func renderLaunchdSystemPlist(options Options, runAsUser string) []byte {
	return renderLaunchdDefinition(options, runAsUser)
}

func renderLaunchdDefinition(options Options, runAsUser string) []byte {
	arguments := agentCommand(options)
	argumentXML := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		argumentXML = append(argumentXML, "    <string>"+xmlText(argument)+"</string>")
	}
	userDefinition := ""
	if runAsUser != "" {
		userDefinition = "  <key>UserName</key>\n  <string>" + xmlText(runAsUser) + "</string>\n"
	}
	definition := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
%s  <key>ProgramArguments</key>
  <array>
%s
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>Umask</key>
  <integer>63</integer>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlText(launchdLabel), userDefinition, strings.Join(argumentXML, "\n"), xmlText(path.Join(options.LogDir, backupLogName)), xmlText(path.Join(options.LogDir, backupErrorLogName)))
	return []byte(definition)
}

func renderWindowsTaskXML(options Options, userID string) ([]byte, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("current Windows user SID is required")
	}

	definition := windowsTaskDefinition{
		Version: windowsTaskXMLLevel,
		XMLNS:   windowsTaskXMLNS,
		RegistrationInfo: windowsRegistrationInfo{
			Description: serviceDescription,
		},
		Triggers: windowsTriggers{
			Logon: windowsLogonTrigger{Enabled: true, UserID: userID},
		},
		Principals: windowsPrincipals{
			Principal: windowsPrincipal{
				ID:        "Author",
				UserID:    userID,
				LogonType: "InteractiveToken",
				RunLevel:  "LeastPrivilege",
			},
		},
		Settings: windowsSettings{
			MultipleInstancesPolicy:    "IgnoreNew",
			DisallowStartIfOnBatteries: false,
			StopIfGoingOnBatteries:     false,
			AllowHardTerminate:         true,
			StartWhenAvailable:         true,
			RunOnlyIfNetworkAvailable:  false,
			Idle: windowsIdleSettings{
				StopOnIdleEnd: false,
				RestartOnIdle: false,
			},
			AllowStartOnDemand: true,
			Enabled:            true,
			Hidden:             false,
			RunOnlyIfIdle:      false,
			WakeToRun:          false,
			ExecutionTimeLimit: "PT0S",
			Priority:           7,
			RestartOnFailure: windowsRestartOnFailure{
				Interval: "PT1M",
				Count:    999,
			},
		},
		Actions: windowsActions{
			Context: "Author",
			Exec: windowsExecAction{
				Command:          options.Executable,
				Arguments:        windowsJoinArguments(agentCommand(options)[1:]),
				WorkingDirectory: options.LogDir,
			},
		},
	}

	payload, err := xml.MarshalIndent(definition, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Windows backup task XML: %w", err)
	}
	payload = append([]byte(xml.Header), payload...)
	payload = append(payload, '\n')
	return payload, nil
}

func renderWindowsSystemTaskXML(options Options) ([]byte, error) {
	definition := windowsSystemTaskDefinition{
		Version: windowsTaskXMLLevel,
		XMLNS:   windowsTaskXMLNS,
		RegistrationInfo: windowsRegistrationInfo{
			Description: serviceDescription,
		},
		Triggers: windowsSystemTriggers{
			Boot: windowsBootTrigger{Enabled: true},
		},
		Principals: windowsPrincipals{
			Principal: windowsPrincipal{
				ID:        "System",
				UserID:    windowsLocalSystemSID,
				LogonType: "ServiceAccount",
				RunLevel:  "HighestAvailable",
			},
		},
		Settings: defaultWindowsSettings(),
		Actions: windowsActions{
			Context: "System",
			Exec: windowsExecAction{
				Command:          options.Executable,
				Arguments:        windowsJoinArguments(agentCommand(options)[1:]),
				WorkingDirectory: options.LogDir,
			},
		},
	}

	payload, err := xml.MarshalIndent(definition, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Windows system backup task XML: %w", err)
	}
	payload = append([]byte(xml.Header), payload...)
	payload = append(payload, '\n')
	return payload, nil
}

func defaultWindowsSettings() windowsSettings {
	return windowsSettings{
		MultipleInstancesPolicy:    "IgnoreNew",
		DisallowStartIfOnBatteries: false,
		StopIfGoingOnBatteries:     false,
		AllowHardTerminate:         true,
		StartWhenAvailable:         true,
		RunOnlyIfNetworkAvailable:  false,
		Idle: windowsIdleSettings{
			StopOnIdleEnd: false,
			RestartOnIdle: false,
		},
		AllowStartOnDemand: true,
		Enabled:            true,
		Hidden:             false,
		RunOnlyIfIdle:      false,
		WakeToRun:          false,
		ExecutionTimeLimit: "PT0S",
		Priority:           7,
		RestartOnFailure: windowsRestartOnFailure{
			Interval: "PT1M",
			Count:    999,
		},
	}
}

func agentCommand(options Options) []string {
	arguments := []string{options.Executable, "backup", "agent", "--service-mode"}
	if options.ConfigDir != "" {
		arguments = append(arguments, "--config-dir", options.ConfigDir)
	}
	if options.StateDir != "" {
		arguments = append(arguments, "--state-dir", options.StateDir)
	}
	if options.LogDir != "" {
		arguments = append(arguments, "--log-dir", options.LogDir)
	}
	return arguments
}

func windowsJoinArguments(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = windowsQuoteArgument(argument)
	}
	return strings.Join(quoted, " ")
}

// windowsQuoteArgument follows CommandLineToArgvW quoting rules, including
// doubling trailing backslashes before the closing quote.
func windowsQuoteArgument(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\n\v\"") {
		return argument
	}
	var result strings.Builder
	result.WriteByte('"')
	backslashes := 0
	for _, character := range argument {
		switch character {
		case '\\':
			backslashes++
		case '"':
			result.WriteString(strings.Repeat("\\", backslashes*2+1))
			result.WriteRune(character)
			backslashes = 0
		default:
			result.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
			result.WriteRune(character)
		}
	}
	result.WriteString(strings.Repeat("\\", backslashes*2))
	result.WriteByte('"')
	return result.String()
}

func systemdQuote(value string) string {
	// A colon command prefix disables environment-variable expansion. Percent
	// specifiers are still expanded by systemd and therefore must be doubled.
	value = strings.ReplaceAll(value, "%", "%%")
	return strconv.Quote(value)
}

func xmlText(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

type windowsTaskDefinition struct {
	XMLName          xml.Name                `xml:"Task"`
	Version          string                  `xml:"version,attr"`
	XMLNS            string                  `xml:"xmlns,attr"`
	RegistrationInfo windowsRegistrationInfo `xml:"RegistrationInfo"`
	Triggers         windowsTriggers         `xml:"Triggers"`
	Principals       windowsPrincipals       `xml:"Principals"`
	Settings         windowsSettings         `xml:"Settings"`
	Actions          windowsActions          `xml:"Actions"`
}

type windowsSystemTaskDefinition struct {
	XMLName          xml.Name                `xml:"Task"`
	Version          string                  `xml:"version,attr"`
	XMLNS            string                  `xml:"xmlns,attr"`
	RegistrationInfo windowsRegistrationInfo `xml:"RegistrationInfo"`
	Triggers         windowsSystemTriggers   `xml:"Triggers"`
	Principals       windowsPrincipals       `xml:"Principals"`
	Settings         windowsSettings         `xml:"Settings"`
	Actions          windowsActions          `xml:"Actions"`
}

type windowsRegistrationInfo struct {
	Description string `xml:"Description"`
}

type windowsTriggers struct {
	Logon windowsLogonTrigger `xml:"LogonTrigger"`
}

type windowsSystemTriggers struct {
	Boot windowsBootTrigger `xml:"BootTrigger"`
}

type windowsBootTrigger struct {
	Enabled bool `xml:"Enabled"`
}

type windowsLogonTrigger struct {
	Enabled bool   `xml:"Enabled"`
	UserID  string `xml:"UserId"`
}

type windowsPrincipals struct {
	Principal windowsPrincipal `xml:"Principal"`
}

type windowsPrincipal struct {
	ID        string `xml:"id,attr"`
	UserID    string `xml:"UserId"`
	LogonType string `xml:"LogonType"`
	RunLevel  string `xml:"RunLevel"`
}

type windowsSettings struct {
	MultipleInstancesPolicy    string                  `xml:"MultipleInstancesPolicy"`
	DisallowStartIfOnBatteries bool                    `xml:"DisallowStartIfOnBatteries"`
	StopIfGoingOnBatteries     bool                    `xml:"StopIfGoingOnBatteries"`
	AllowHardTerminate         bool                    `xml:"AllowHardTerminate"`
	StartWhenAvailable         bool                    `xml:"StartWhenAvailable"`
	RunOnlyIfNetworkAvailable  bool                    `xml:"RunOnlyIfNetworkAvailable"`
	Idle                       windowsIdleSettings     `xml:"IdleSettings"`
	AllowStartOnDemand         bool                    `xml:"AllowStartOnDemand"`
	Enabled                    bool                    `xml:"Enabled"`
	Hidden                     bool                    `xml:"Hidden"`
	RunOnlyIfIdle              bool                    `xml:"RunOnlyIfIdle"`
	WakeToRun                  bool                    `xml:"WakeToRun"`
	ExecutionTimeLimit         string                  `xml:"ExecutionTimeLimit"`
	Priority                   int                     `xml:"Priority"`
	RestartOnFailure           windowsRestartOnFailure `xml:"RestartOnFailure"`
}

type windowsIdleSettings struct {
	StopOnIdleEnd bool `xml:"StopOnIdleEnd"`
	RestartOnIdle bool `xml:"RestartOnIdle"`
}

type windowsRestartOnFailure struct {
	Interval string `xml:"Interval"`
	Count    int    `xml:"Count"`
}

type windowsActions struct {
	Context string            `xml:"Context,attr"`
	Exec    windowsExecAction `xml:"Exec"`
}

type windowsExecAction struct {
	Command          string `xml:"Command"`
	Arguments        string `xml:"Arguments"`
	WorkingDirectory string `xml:"WorkingDirectory"`
}
