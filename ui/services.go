package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
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
	dbSizes   string // database sizes (estimated)
	user      string // default user
	unit      string // systemd unit name
}

// showServiceDashboard displays the DB services status panel
func (a *App) showServiceDashboard() {
	// ── Header ──
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText(fmt.Sprintf(" [::b][#cba6f7]%s Database Services[-][-]  [#a6adc8]System status & management[-]", iconServices))

	// ── Content ──
	content := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	content.SetBackgroundColor(bg)
	content.SetBorder(true).
		SetBorderColor(surface1).
		SetTitleColor(mauve)

	// Initial loading state
	content.SetText(fmt.Sprintf("\n\n\n\n          [::b][#89b4fa]%s Loading service information...[-][-]", iconRefresh))

	// ── Footer ──
	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	screenW, _ := a.getScreenSize()
	switch {
	case screenW < 95:
		footer.SetText(fmt.Sprintf("  [yellow]1[-] MySQL  │  [yellow]2[-] PG  │  [yellow]R[-] %s  │  [yellow]Esc[-] Back %s", iconRefresh, iconBack))
	default:
		footer.SetText(fmt.Sprintf("  [yellow]1[-] Toggle MySQL  │  [yellow]2[-] Toggle PostgreSQL  │  [yellow]R[-] %s  │  [yellow]Esc[-] Back %s", iconRefresh, iconBack))
	}

	// ── Layout ──
	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(footer, 1, 0, false)

	// Variable to hold info for key bindings
	var mysqlInfo, pgInfo *serviceInfo

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
		case tcell.KeyEnter:
			a.showConnectServiceModal()
			return nil
		}

		switch event.Rune() {
		case 'c', 'C':
			a.showConnectServiceModal()
			return nil
		case '1':
			if mysqlInfo != nil {
				a.toggleService(mysqlInfo)
			}
			return nil
		case '2':
			if pgInfo != nil {
				a.toggleService(pgInfo)
			}
			return nil
		case 'r', 'R':
			// Refresh the service dashboard entirely
			a.pages.RemovePage("services")
			a.showServiceDashboard()
			return nil
		}
		return event
	})

	a.pages.AddAndSwitchToPage("services", layout, true)
	a.app.SetFocus(content)

	// ── Fetch Data in Background ──
	go func() {
		// Gather info in background (can be slow)
		mInfo := getServiceInfo("MySQL", "mysql", "mysqld", "mysql")
		pInfo := getServiceInfo("PostgreSQL", "postgresql", "postgres", "postgresql")

		// Update UI on main thread
		a.app.QueueUpdateDraw(func() {
			// Update the closure variables for key bindings
			mysqlInfo = mInfo
			pgInfo = pInfo

			var sb strings.Builder

			// MySQL section
			writeServiceSection(&sb, mInfo)
			sb.WriteString("\n")
			// PostgreSQL section
			writeServiceSection(&sb, pInfo)

			sb.WriteString(fmt.Sprintf("\n\n[#6c7086]Press [yellow]1[-][#6c7086] to toggle MySQL  │  [yellow]2[-][#6c7086] to toggle PostgreSQL[-]"))
			sb.WriteString(fmt.Sprintf("\n[#6c7086]Press [yellow]C[-][#6c7086] to Connect  │  [yellow]R[-][#6c7086] to Refresh  │  [yellow]Esc[-][#6c7086] Back %s[-]", iconBack))

			content.SetText(sb.String())
		})
	}()
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
	if info.dbSizes != "" {
		sb.WriteString(fmt.Sprintf("     [#a6adc8]DB Sizes:[-]   %s\n", info.dbSizes))
	}

	if info.active {
		sb.WriteString("     [#6c7086]Action: Press key to [red]stop[-][#6c7086] this service ■[-]\n")
	} else {
		sb.WriteString("     [#6c7086]Action: Press key to [green]start[-][#6c7086] this service ▶[-]\n")
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
		info.dbSizes = getServiceDatabaseSizes(displayName)
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

// getProcessRAM reads VmRSS from /proc/<pid>/status and also sums child
// process RSS (important for PostgreSQL which forks worker processes).
func getProcessRAM(pid string) string {
	// First get the main process RSS
	mainRSS := readVmRSS(pid)

	// Sum RSS of child processes (pgrep -P <pid>)
	childOut := runCmd("pgrep", "-P", pid)
	totalKB := mainRSS
	if childOut != "" {
		for _, childPid := range strings.Fields(childOut) {
			childPid = strings.TrimSpace(childPid)
			if childPid != "" && childPid != pid {
				totalKB += readVmRSS(childPid)
			}
		}
	}

	if totalKB == 0 {
		return "—"
	}
	return utils.FormatBytes(totalKB * 1024)
}

// readVmRSS reads VmRSS in kB from /proc/<pid>/status.
func readVmRSS(pid string) uint64 {
	out := runCmd("cat", fmt.Sprintf("/proc/%s/status", pid))
	if out == "" {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				var val uint64
				fmt.Sscanf(parts[1], "%d", &val)
				return val
			}
		}
	}
	return 0
}

// getServiceDatabases lists databases for a running service
func getServiceDatabases(serviceName string) string {
	switch serviceName {
	case "MySQL":
		out := runCmd("mysql", "-u", "root", "-e", "SHOW DATABASES;")
		if out == "" {
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

// getServiceDatabaseSizes returns a formatted string of database sizes.
func getServiceDatabaseSizes(serviceName string) string {
	switch serviceName {
	case "MySQL":
		out := runCmd("mysql", "-u", "root", "-N", "-e",
			"SELECT table_schema, SUM(data_length + index_length) FROM information_schema.tables GROUP BY table_schema ORDER BY SUM(data_length + index_length) DESC;")
		if out == "" {
			out = runCmd("mysql", "--defaults-file=/etc/mysql/debian.cnf", "-N", "-e",
				"SELECT table_schema, SUM(data_length + index_length) FROM information_schema.tables GROUP BY table_schema ORDER BY SUM(data_length + index_length) DESC;")
		}
		return parseDBSizeOutput(out)
	case "PostgreSQL":
		out := runCmd("sudo", "-u", "postgres", "psql", "-t", "-A", "-c",
			"SELECT datname, pg_database_size(datname) FROM pg_database WHERE datistemplate = false ORDER BY pg_database_size(datname) DESC")
		return parseDBSizeOutput(out)
	}
	return ""
}

func parseDBSizeOutput(out string) string {
	if out == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var entries []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Try pipe separator (psql) or tab separator (mysql)
		var name, sizeStr string
		if parts := strings.SplitN(line, "|", 2); len(parts) == 2 {
			name = strings.TrimSpace(parts[0])
			sizeStr = strings.TrimSpace(parts[1])
		} else if parts := strings.SplitN(line, "\t", 2); len(parts) == 2 {
			name = strings.TrimSpace(parts[0])
			sizeStr = strings.TrimSpace(parts[1])
		} else {
			continue
		}
		if name == "" || name == "template0" || name == "template1" {
			continue
		}
		var sizeBytes uint64
		fmt.Sscanf(sizeStr, "%d", &sizeBytes)
		if sizeBytes > 0 {
			entries = append(entries, fmt.Sprintf("%s (%s)", name, utils.FormatBytes(sizeBytes)))
		} else {
			entries = append(entries, name)
		}
	}
	if len(entries) > 6 {
		return strings.Join(entries[:6], ", ") + fmt.Sprintf(" (+%d more)", len(entries)-6)
	}
	return strings.Join(entries, ", ")
}

// toggleService starts or stops a database service
func (a *App) toggleService(info *serviceInfo) {
	if !info.installed {
		a.ShowAlert(fmt.Sprintf("%s %s is not installed.\n\nInstall it first:\n  sudo apt install %s-server",
			iconWarn,
			info.name, strings.ToLower(info.name)), "services")
		return
	}

	action := "start"
	if info.active {
		action = "stop"
	}

	// 1. Try non-interactive sudo first (in case of cached credentials or NOPASSWD)
	if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
		a.confirmAndRunServiceCmd(action, info, "")
		return
	}

	// 2. If that fails, prompt for password
	a.showSudoPasswordPrompt(action, info)
}

// showSudoPasswordPrompt displays a modal input for the sudo password
func (a *App) showSudoPasswordPrompt(action string, info *serviceInfo) {
	form := tview.NewForm()

	actionTitle := strings.ToUpper(action[:1]) + action[1:]
	form.SetTitle(fmt.Sprintf(" %s Sudo Password Required for %s %s ", iconWarn, actionTitle, info.name))
	form.SetTitleColor(red)
	form.SetBorder(true)
	form.SetBorderColor(red)

	form.AddPasswordField("Password", "", 32, '*', nil)

	form.AddButton("Run", func() {
		passwordItem := form.GetFormItemByLabel("Password")
		passwordField, ok := passwordItem.(*tview.InputField)
		if !ok {
			a.ShowAlert(fmt.Sprintf("%s Could not read sudo password input.", iconWarn), "services")
			return
		}
		password := strings.TrimSpace(passwordField.GetText())
		if password == "" {
			a.ShowAlert(fmt.Sprintf("%s Sudo password is required to continue.", iconInfo), "sudoPrompt")
			return
		}

		a.pages.RemovePage("sudoPrompt")
		a.runServiceCmdWithSudo(action, info, password)
	})

	form.AddButton("Cancel", func() {
		a.pages.RemovePage("sudoPrompt")
		a.pages.ShowPage("services")
	})

	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle)
	form.SetButtonBackgroundColor(surface1)
	form.SetButtonTextColor(green)
	form.SetLabelColor(text)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("sudoPrompt")
			a.pages.ShowPage("services")
			return nil
		}
		return event
	})

	modalW, modalH := a.modalSize(44, 72, 9, 13)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("sudoPrompt", grid, true, true)
	a.app.SetFocus(form)
}

// confirmAndRunServiceCmd shows confirmation if no password needed, then runs
func (a *App) confirmAndRunServiceCmd(action string, info *serviceInfo, password string) {
	actionTitle := strings.ToUpper(action[:1]) + action[1:]
	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s %s %s?\n\nThis will run:\n  sudo systemctl %s %s",
			iconServices, actionTitle, info.name, action, info.unit)).
		AddButtons([]string{"  Yes  ", "  No  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.RemovePage("serviceConfirm")
			if buttonIndex == 0 {
				a.runServiceCmdWithSudo(action, info, password)
			} else {
				a.pages.ShowPage("services")
			}
		})

	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)

	a.pages.AddPage("serviceConfirm", modal, true, true)
	a.app.SetFocus(modal)
}

// runServiceCmdWithSudo executes the systemctl command, piping password if provided
func (a *App) runServiceCmdWithSudo(action string, info *serviceInfo, password string) {
	actionTitle := strings.ToUpper(action[:1]) + action[1:]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var canceled atomic.Bool
	a.showLoadingModal(fmt.Sprintf("%s %s %s...", iconServices, actionTitle, info.name),
		withLoadingCancel("Press Esc to cancel.", func() {
			canceled.Store(true)
			cancel()
		}))

	go func() {
		defer cancel()

		var cmd *exec.Cmd
		if password != "" {
			// Use -p "" to suppress prompts and keep output cleaner for error handling.
			cmd = exec.CommandContext(ctx, "sudo", "-S", "-k", "-p", "", "systemctl", action, info.unit)
			cmd.Stdin = strings.NewReader(password + "\n")
		} else {
			cmd = exec.CommandContext(ctx, "sudo", "-n", "systemctl", action, info.unit)
		}

		// Capture output for error reporting
		out, err := cmd.CombinedOutput()
		outStr := strings.TrimSpace(string(out))

		// Update UI on main thread
		a.app.QueueUpdateDraw(func() {
			// Remove loading modal
			a.pages.RemovePage("loading")

			if canceled.Load() {
				a.ShowAlert(fmt.Sprintf("%s %s %s canceled.", iconWarn, actionTitle, info.name), "services")
				return
			}

			if err != nil {
				errMsg := err.Error()
				if ctx.Err() == context.DeadlineExceeded {
					errMsg = "Timed out while waiting for sudo/systemctl"
				}
				if strings.Contains(outStr, "incorrect password") || strings.Contains(outStr, "try again") {
					errMsg = "Incorrect password"
				}
				if strings.Contains(outStr, "a password is required") {
					errMsg = "Sudo requires a password"
				}
				if outStr == "" {
					outStr = "(no output)"
				}

				a.ShowAlert(fmt.Sprintf("%s Failed to %s %s:\n\n%s\n%s",
					iconFail, action, info.name, errMsg, outStr), "services")
				return
			}

			a.pages.RemovePage("services")
			a.showServiceDashboard()
			a.ShowAlert(fmt.Sprintf("%s %s %s succeeded.", iconSuccess, info.name, action), "services")
		})
	}()
}

type loadingModalOptions struct {
	cancelText string
	onCancel   func()
}

type loadingModalOption func(*loadingModalOptions)

func withLoadingCancel(cancelText string, onCancel func()) loadingModalOption {
	return func(opts *loadingModalOptions) {
		opts.cancelText = cancelText
		opts.onCancel = onCancel
	}
}

// showLoadingModal displays a loading spinner with optional cancellation.
func (a *App) showLoadingModal(message string, options ...loadingModalOption) {
	opts := loadingModalOptions{
		cancelText: "Please wait; this step cannot be cancelled safely.",
	}
	for _, option := range options {
		option(&opts)
	}

	modalText := fmt.Sprintf("\n%s %s\n\n%s", iconRefresh, message, opts.cancelText)
	modal := tview.NewModal().
		SetText(modalText).
		SetBackgroundColor(bg).
		SetTextColor(text)

	if opts.onCancel != nil {
		cancel := opts.onCancel
		modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				a.pages.RemovePage("loading")
				a.updateStatusBar("[yellow]Operation canceled.[-]", 0)
				cancel()
				return nil
			}
			return event
		})
	}

	a.pages.AddPage("loading", modal, true, true)
	a.app.SetFocus(modal)
}

// runCmd runs a command with a 3-second timeout and returns stdout, or empty on error
func runCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")

	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

// showConnectServiceModal displays a modal to connect to a database service.
// It includes a Browse button to discover databases in the running instance.
func (a *App) showConnectServiceModal() {
	form := tview.NewForm()
	form.SetTitle(fmt.Sprintf(" %s Connect to Database Service ", iconConnect))
	form.SetTitleColor(blue)
	form.SetBorder(true)
	form.SetBorderColor(blue)

	// Service Type
	form.AddDropDown("Service", []string{"MySQL", "PostgreSQL"}, 0, nil)

	// Database Name
	form.AddInputField("Database", "", 20, nil, nil)

	// User
	form.AddInputField("User", "root", 20, nil, nil)

	// Password
	form.AddPasswordField("Password", "", 20, '*', nil)

	// Update defaults when service changes
	serviceDropDown := form.GetFormItemByLabel("Service").(*tview.DropDown)
	serviceDropDown.SetSelectedFunc(func(text string, index int) {
		userInput := form.GetFormItemByLabel("User").(*tview.InputField)
		if text == "PostgreSQL" {
			userInput.SetText("postgres")
		} else {
			userInput.SetText("root")
		}
	})

	form.AddButton("Connect", func() {
		_, service := serviceDropDown.GetCurrentOption()
		dbName := strings.TrimSpace(form.GetFormItemByLabel("Database").(*tview.InputField).GetText())
		user := strings.TrimSpace(form.GetFormItemByLabel("User").(*tview.InputField).GetText())
		password := strings.TrimSpace(form.GetFormItemByLabel("Password").(*tview.InputField).GetText())

		var dbType config.DBType
		var dsn string
		if service == "PostgreSQL" {
			dbType = config.PostgreSQL
			dsn = fmt.Sprintf("postgres://%s:%s@localhost:5432/%s?sslmode=disable", user, password, dbName)
		} else {
			dbType = config.MySQL
			dsn = fmt.Sprintf("%s:%s@tcp(localhost:3306)/%s", user, password, dbName)
		}

		a.pages.RemovePage("connectService")
		a.showLoadingModal(fmt.Sprintf("Connecting to %s...", dbName))

		go func() {
			err := a.DirectConnect(dbType, dsn, dbName)
			a.app.QueueUpdateDraw(func() {
				a.pages.RemovePage("loading")
				if err != nil {
					a.ShowAlert(fmt.Sprintf("Connection failed:\n\n%v", err), "services")
				} else {
					a.pages.RemovePage("services")
					a.pages.ShowPage("main")
					a.updateStatusBar(fmt.Sprintf("[green]Connected to %s[-]", dbName), 0)
				}
			})
		}()
	})

	form.AddButton("Browse DBs", func() {
		_, service := serviceDropDown.GetCurrentOption()
		a.showDatabasePicker(service, form)
	})

	form.AddButton("Cancel", func() {
		a.pages.RemovePage("connectService")
		a.pages.ShowPage("services")
	})

	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle)
	form.SetButtonBackgroundColor(surface1)
	form.SetButtonTextColor(green)
	form.SetLabelColor(text)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("connectService")
			a.pages.ShowPage("services")
			return nil
		}
		return event
	})

	modalW, modalH := a.modalSize(50, 80, 14, 18)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("connectService", grid, true, true)
	a.app.SetFocus(form)
}

// showDatabasePicker fetches the list of databases from a running service
// and shows a selection list so the user can pick one.
func (a *App) showDatabasePicker(serviceName string, parentForm *tview.Form) {
	var dbs []string
	switch serviceName {
	case "MySQL":
		out := runCmd("mysql", "-u", "root", "-N", "-e", "SHOW DATABASES;")
		if out == "" {
			out = runCmd("mysql", "--defaults-file=/etc/mysql/debian.cnf", "-N", "-e", "SHOW DATABASES;")
		}
		if out != "" {
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				l = strings.TrimSpace(l)
				if l != "" {
					dbs = append(dbs, l)
				}
			}
		}
	case "PostgreSQL":
		out := runCmd("sudo", "-u", "postgres", "psql", "-t", "-A", "-c",
			"SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname")
		if out != "" {
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				l = strings.TrimSpace(l)
				if l != "" {
					dbs = append(dbs, l)
				}
			}
		}
	}

	if len(dbs) == 0 {
		a.ShowAlert(fmt.Sprintf("%s Could not discover databases for %s.\n\nThe service may not be running, or authentication is required.", iconWarn, serviceName), "connectService")
		return
	}

	// Show a picker list
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Select Database (%s) ", iconConnect, serviceName)).
		SetBorderColor(blue).
		SetTitleColor(mauve).
		SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	for _, db := range dbs {
		dbName := db
		list.AddItem(fmt.Sprintf("  %s  %s", iconConnect, dbName), "", 0, func() {
			// Set the database field in the parent form
			dbInput := parentForm.GetFormItemByLabel("Database").(*tview.InputField)
			dbInput.SetText(dbName)
			a.pages.RemovePage("dbPicker")
			a.app.SetFocus(parentForm)
		})
	}

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("dbPicker")
			a.app.SetFocus(parentForm)
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf("  [yellow]Enter[-] Select  │  [yellow]Esc[-] Back %s  │  [#6c7086]%d databases found[-]", iconBack, len(dbs)))
	footer.SetBackgroundColor(crust)

	pickerFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(40, 60, 10, 20)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(pickerFlex, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("dbPicker", grid, true, true)
	a.app.SetFocus(list)
}
