package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// serviceInfo holds detected info about a database service
type serviceInfo struct {
	name      string // "MySQL" or "PostgreSQL"
	installed bool
	active    bool   // systemctl is-active
	port      string // from ss -tlnp
	pid       string // from systemctl show
	ram       string // from /proc/<pid>/status VmRSS
	version   string // from mysql --version / psql --version
	databases string // listed databases
	user      string // default user
	unit      string // systemd unit name
}

// showServiceDashboard displays the DB services status panel
func (a *App) showServiceDashboard() {
	// Gather info in background-safe way (short timeout commands)
	mysqlInfo := getServiceInfo("MySQL", "mysql", "mysqld", "mysql")
	pgInfo := getServiceInfo("PostgreSQL", "postgresql", "postgres", "postgresql")

	// ── Header ──
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText(`
[::b][#cba6f7]━━━ Database Services ━━━[-][-]
[#a6adc8]System service status & management[-]`)

	// ── Build content ──
	content := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	content.SetBackgroundColor(bg)
	content.SetBorder(true).
		SetBorderColor(surface1).
		SetTitleColor(mauve)

	var sb strings.Builder

	// MySQL section
	writeServiceSection(&sb, mysqlInfo)
	sb.WriteString("\n")
	// PostgreSQL section
	writeServiceSection(&sb, pgInfo)

	sb.WriteString("\n\n[#6c7086]Press [yellow]1[-][#6c7086] to toggle MySQL  │  [yellow]2[-][#6c7086] to toggle PostgreSQL[-]")
	sb.WriteString("\n[#6c7086]Press [yellow]R[-][#6c7086] to refresh  │  [yellow]Esc[-][#6c7086] to go back[-]")

	content.SetText(sb.String())

	// ── Footer ──
	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText("  [yellow]1[-] Toggle MySQL  │  [yellow]2[-] Toggle PostgreSQL  │  [yellow]R[-] Refresh  │  [yellow]Esc[-] Back")

	// ── Layout ──
	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 4, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(footer, 1, 0, false)

	// ── Key bindings ──
	content.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			a.pages.RemovePage("services")
			front, _ := a.pages.GetFrontPage()
			if front == "" {
				if a.db != nil {
					a.pages.ShowPage("main")
				} else {
					a.showDashboard()
				}
			}
			return nil
		}

		switch event.Rune() {
		case '1':
			a.toggleService(mysqlInfo)
			return nil
		case '2':
			a.toggleService(pgInfo)
			return nil
		case 'r', 'R':
			// Refresh the service dashboard
			a.pages.RemovePage("services")
			a.showServiceDashboard()
			return nil
		}
		return event
	})

	a.pages.AddAndSwitchToPage("services", layout, true)
	a.app.SetFocus(content)
}

// writeServiceSection writes a formatted section for one service
func writeServiceSection(sb *strings.Builder, info *serviceInfo) {
	// Icon and status
	var statusIcon, statusText string
	if !info.installed {
		statusIcon = "[#6c7086]○[-]"
		statusText = "[#6c7086]Not Installed[-]"
	} else if info.active {
		statusIcon = "[green]●[-]"
		statusText = "[green]Active[-]"
	} else {
		statusIcon = "[red]○[-]"
		statusText = "[red]Inactive[-]"
	}

	var nameColor string
	switch info.name {
	case "MySQL":
		nameColor = "#f9e2af"
	case "PostgreSQL":
		nameColor = "#89b4fa"
	}

	sb.WriteString(fmt.Sprintf("\n  %s  [::b][%s]%s[-][-]  %s\n", statusIcon, nameColor, info.name, statusText))

	if !info.installed {
		sb.WriteString(fmt.Sprintf("     [#6c7086]Not found. Install with: sudo apt install %s[-]\n", strings.ToLower(info.name)+"-server"))
		return
	}

	sb.WriteString(fmt.Sprintf("     [#a6adc8]Version:[-]    %s\n", info.version))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]Unit:[-]       %s\n", info.unit))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]Port:[-]       %s\n", info.port))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]PID:[-]        %s\n", info.pid))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]RAM:[-]        %s\n", info.ram))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]User:[-]       %s\n", info.user))
	if info.databases != "" {
		sb.WriteString(fmt.Sprintf("     [#a6adc8]Databases:[-]  %s\n", info.databases))
	}

	if info.active {
		sb.WriteString("     [#6c7086]Action: Press key to [red]stop[-][#6c7086] this service[-]\n")
	} else {
		sb.WriteString("     [#6c7086]Action: Press key to [green]start[-][#6c7086] this service[-]\n")
	}
}

// getServiceInfo gathers information about a database service
func getServiceInfo(displayName, cmdName, processName, unitName string) *serviceInfo {
	info := &serviceInfo{
		name: displayName,
		unit: unitName,
	}

	// Check if installed
	_, err := exec.LookPath(cmdName)
	if err != nil {
		// Try alternate names
		switch cmdName {
		case "mysql":
			_, err = exec.LookPath("mariadb")
			if err == nil {
				cmdName = "mariadb"
			}
		case "postgresql":
			_, err = exec.LookPath("psql")
			if err == nil {
				cmdName = "psql"
			}
		}
		if err != nil {
			info.installed = false
			return info
		}
	}
	info.installed = true

	// Check version
	info.version = runCmd(cmdName, "--version")
	if info.version == "" {
		info.version = "unknown"
	} else {
		// Take first line only
		if lines := strings.SplitN(info.version, "\n", 2); len(lines) > 0 {
			info.version = strings.TrimSpace(lines[0])
		}
	}

	// Check systemctl status
	// Try multiple unit names
	unitNames := getUnitNames(unitName)
	for _, u := range unitNames {
		out := runCmd("systemctl", "is-active", u)
		out = strings.TrimSpace(out)
		if out == "active" {
			info.active = true
			info.unit = u
			break
		}
		// If we find a valid unit (even if inactive), use it
		if out == "inactive" || out == "failed" {
			info.unit = u
		}
	}

	// Get PID
	pid := runCmd("systemctl", "show", "--property=MainPID", "--value", info.unit)
	pid = strings.TrimSpace(pid)
	if pid != "" && pid != "0" {
		info.pid = pid
		// Get RAM from /proc/<pid>/status
		info.ram = getProcessRAM(pid)
	} else {
		info.pid = "—"
		info.ram = "—"
	}

	// Get port
	info.port = getServicePort(processName)
	if info.port == "" {
		// Use defaults
		switch displayName {
		case "MySQL":
			info.port = "3306 (default)"
		case "PostgreSQL":
			info.port = "5432 (default)"
		}
	}

	// Get user
	switch displayName {
	case "MySQL":
		info.user = "root (default)"
	case "PostgreSQL":
		info.user = "postgres (default)"
	}

	// Get databases (only if service is active)
	if info.active {
		info.databases = getServiceDatabases(displayName)
	}

	return info
}

// getUnitNames returns possible systemd unit names for a service
func getUnitNames(base string) []string {
	switch base {
	case "mysql":
		return []string{"mysql", "mysqld", "mariadb"}
	case "postgresql":
		// Try versioned units too
		units := []string{"postgresql"}
		for v := 17; v >= 12; v-- {
			units = append(units, fmt.Sprintf("postgresql@%d-main", v))
		}
		return units
	default:
		return []string{base}
	}
}

// getServicePort tries to find the listening port for a service process
func getServicePort(processName string) string {
	out := runCmd("ss", "-tlnp")
	if out == "" {
		return ""
	}

	var ports []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(line), processName) {
			// Extract port from the Local Address column
			fields := strings.Fields(line)
			for _, f := range fields {
				if strings.Contains(f, ":") {
					parts := strings.Split(f, ":")
					port := parts[len(parts)-1]
					if port != "" && port != "*" {
						ports = append(ports, port)
					}
				}
			}
		}
	}

	if len(ports) == 0 {
		return ""
	}
	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	return strings.Join(unique, ", ")
}

// getProcessRAM reads VmRSS from /proc/<pid>/status
func getProcessRAM(pid string) string {
	out := runCmd("cat", fmt.Sprintf("/proc/%s/status", pid))
	if out == "" {
		return "—"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return strings.Join(parts[1:], " ")
			}
		}
	}
	return "—"
}

// getServiceDatabases lists databases for a running service
func getServiceDatabases(serviceName string) string {
	switch serviceName {
	case "MySQL":
		out := runCmd("mysql", "-u", "root", "-e", "SHOW DATABASES;")
		if out == "" {
			// Try without auth
			out = runCmd("mysql", "--defaults-file=/etc/mysql/debian.cnf", "-e", "SHOW DATABASES;")
		}
		if out != "" {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var dbs []string
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if l != "" && l != "Database" && !strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "|") {
					dbs = append(dbs, l)
				}
			}
			if len(dbs) > 8 {
				return strings.Join(dbs[:8], ", ") + fmt.Sprintf(" (+%d more)", len(dbs)-8)
			}
			return strings.Join(dbs, ", ")
		}
		return "[#6c7086]auth required[-]"
	case "PostgreSQL":
		out := runCmd("sudo", "-u", "postgres", "psql", "-l", "-t", "-A")
		if out != "" {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var dbs []string
			for _, l := range lines {
				parts := strings.SplitN(l, "|", 2)
				if len(parts) > 0 {
					name := strings.TrimSpace(parts[0])
					if name != "" && name != "template0" && name != "template1" {
						dbs = append(dbs, name)
					}
				}
			}
			if len(dbs) > 8 {
				return strings.Join(dbs[:8], ", ") + fmt.Sprintf(" (+%d more)", len(dbs)-8)
			}
			return strings.Join(dbs, ", ")
		}
		return "[#6c7086]auth required[-]"
	}
	return ""
}

// toggleService starts or stops a database service
func (a *App) toggleService(info *serviceInfo) {
	if !info.installed {
		a.ShowAlert(fmt.Sprintf("%s is not installed.\n\nInstall it first:\n  sudo apt install %s-server",
			info.name, strings.ToLower(info.name)), "services")
		return
	}

	action := "start"
	actionPast := "started"
	if info.active {
		action = "stop"
		actionPast = "stopped"
	}

	// Show confirmation
	actionTitle := strings.ToUpper(action[:1]) + action[1:]
	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s %s?\n\nThis will run:\n  sudo systemctl %s %s",
			actionTitle, info.name, action, info.unit)).
		AddButtons([]string{"  Yes  ", "  No  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.RemovePage("serviceConfirm")
			if buttonIndex == 0 {
				// Run the command
				go func() {
					out, err := exec.Command("sudo", "systemctl", action, info.unit).CombinedOutput()
					a.app.QueueUpdateDraw(func() {
						if err != nil {
							a.ShowAlert(fmt.Sprintf("Failed to %s %s:\n\n%s\n%s\n\nTry running manually:\n  sudo systemctl %s %s",
								action, info.name, err.Error(), string(out), action, info.unit), "services")
						} else {
							a.ShowAlert(fmt.Sprintf("✓ %s %s successfully!\n\nRefreshing...", info.name, actionPast), "services")
							// Refresh dashboard after a brief pause
							time.Sleep(500 * time.Millisecond)
							a.app.QueueUpdateDraw(func() {
								a.pages.RemovePage("alert")
								a.pages.RemovePage("services")
								a.showServiceDashboard()
							})
						}
					})
				}()
			}
		})

	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)

	a.pages.AddPage("serviceConfirm", modal, true, true)
	a.app.SetFocus(modal)
}

// runCmd runs a command with a 3-second timeout and returns stdout, or empty on error
func runCmd(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")

	// Use a channel + goroutine to enforce timeout
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := cmd.Output()
		ch <- result{out, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return ""
		}
		return strings.TrimSpace(string(r.out))
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return ""
	}
}

// getQuickStatus returns a short colored status string for the dashboard header
func getQuickStatus(unitName string) string {
	units := getUnitNames(unitName)
	for _, u := range units {
		out := runCmd("systemctl", "is-active", u)
		out = strings.TrimSpace(out)
		switch out {
		case "active":
			var color string
			switch unitName {
			case "mysql":
				color = "#f9e2af"
			case "postgresql":
				color = "#89b4fa"
			}
			name := strings.ToUpper(unitName[:1]) + unitName[1:]
			return fmt.Sprintf("[green]●[-] [%s]%s[-]", color, name)
		case "inactive", "failed":
			name := strings.ToUpper(unitName[:1]) + unitName[1:]
			return fmt.Sprintf("[red]○[-] [#6c7086]%s[-]", name)
		}
	}
	// Not installed
	name := strings.ToUpper(unitName[:1]) + unitName[1:]
	return fmt.Sprintf("[#6c7086]○ %s (n/a)[-]", name)
}
