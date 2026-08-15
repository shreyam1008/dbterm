package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
	"github.com/shreyam1008/dbterm/internal/format"
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
		footer.SetText(fmt.Sprintf(" [yellow]1[-] MySQL │ [yellow]2[-] PG │ [yellow]C[-] Connect │ [yellow]R[-] %s │ [yellow]Esc[-] %s", iconRefresh, iconBack))
	default:
		footer.SetText(fmt.Sprintf(" [yellow]1[-] Toggle MySQL  │  [yellow]2[-] Toggle PostgreSQL  │  [yellow]C/Enter[-] Connect  │  [yellow]R[-] %s  │  [yellow]Esc[-] Back %s", iconRefresh, iconBack))
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
		// Probe both services concurrently so one missing or slow service cannot
		// delay the other. Credentialed database discovery belongs in the
		// connection form and is intentionally not part of this status refresh.
		mysqlResult := make(chan *serviceInfo, 1)
		postgresResult := make(chan *serviceInfo, 1)
		go func() { mysqlResult <- getServiceInfo("MySQL", "mysql", "mysqld", "mysql") }()
		go func() { postgresResult <- getServiceInfo("PostgreSQL", "postgresql", "postgres", "postgresql") }()
		mInfo := <-mysqlResult
		pInfo := <-postgresResult

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

			sb.WriteString("\n  [#6c7086]Database names are loaded only when you choose Connect, using the credentials you enter.[-]")

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
		sb.WriteString(fmt.Sprintf("     [#6c7086]Not found. Install with: sudo apt install %s[-]\n", serviceInstallPackage(info.name)))
		return
	}

	sb.WriteString(fmt.Sprintf("     [#a6adc8]Version:[-]    %s\n", info.version))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]Unit:[-]       %s\n", info.unit))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]Port:[-]       %s\n", info.port))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]PID:[-]        %s\n", info.pid))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]RAM:[-]        %s\n", info.ram))
	sb.WriteString(fmt.Sprintf("     [#a6adc8]User:[-]       %s\n", info.user))
	actionKey := "1"
	if info.name == "PostgreSQL" {
		actionKey = "2"
	}
	if info.active {
		sb.WriteString(fmt.Sprintf("     [yellow]%s[-] [red]Stop service[-]  [#6c7086]■[-]\n", actionKey))
	} else {
		sb.WriteString(fmt.Sprintf("     [yellow]%s[-] [green]Start service[-]  [#6c7086]▶[-]\n", actionKey))
	}
}

func serviceInstallPackage(serviceName string) string {
	if serviceName == "PostgreSQL" {
		return "postgresql"
	}
	return "mysql-server"
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

	// Version and local process probes must stay quick. Service details are a
	// dashboard aid, not a reason to block navigation for several seconds.
	info.version = runServiceProbeCmd(cmdName, "--version")
	if info.version == "" {
		info.version = "unknown"
	} else {
		info.version = compactServiceVersion(displayName, info.version)
	}

	// Check candidate systemd units. A single probe returns load state, active
	// state, and PID; the old implementation ran separate commands and retried
	// inactive units until each three-second timeout expired.
	selected := probeSystemdUnits(getUnitNames(unitName))
	if selected.loaded {
		info.unit = selected.unit
		info.active = selected.active
	}

	pid := strings.TrimSpace(selected.pid)
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

	return info
}

func compactServiceVersion(serviceName, raw string) string {
	line := strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0])
	fields := strings.Fields(line)
	if serviceName == "MySQL" {
		for index, field := range fields {
			if field == "Distrib" && index+1 < len(fields) {
				return "MariaDB " + strings.TrimRight(fields[index+1], ",")
			}
		}
	}
	for index, field := range fields {
		switch {
		case serviceName == "PostgreSQL" && field == "(PostgreSQL)" && index+1 < len(fields):
			return "PostgreSQL " + strings.TrimRight(fields[index+1], ",")
		case serviceName == "MySQL" && field == "Ver" && index+1 < len(fields):
			return "MySQL " + strings.TrimRight(fields[index+1], ",")
		}
	}
	if len(line) > 58 {
		return line[:55] + "..."
	}
	return line
}

type systemdUnitProbe struct {
	unit   string
	loaded bool
	active bool
	pid    string
}

func probeSystemdUnit(unit string) systemdUnitProbe {
	probe := systemdUnitProbe{unit: unit}
	out := runServiceProbeCmd("systemctl", "show", unit, "--no-pager",
		"--property=LoadState", "--property=ActiveState", "--property=MainPID")
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			probe.loaded = value == "loaded"
		case "ActiveState":
			probe.active = value == "active"
		case "MainPID":
			probe.pid = value
		}
	}
	return probe
}

func probeSystemdUnits(units []string) systemdUnitProbe {
	if len(units) == 0 {
		return systemdUnitProbe{}
	}
	results := make(chan systemdUnitProbe, len(units))
	for _, unit := range units {
		unit := unit
		go func() { results <- probeSystemdUnit(unit) }()
	}

	byUnit := make(map[string]systemdUnitProbe, len(units))
	for range units {
		probe := <-results
		byUnit[probe.unit] = probe
	}

	var selected systemdUnitProbe
	for _, unit := range units {
		probe := byUnit[unit]
		if !probe.loaded {
			continue
		}
		if !selected.loaded || probe.active {
			selected = probe
		}
		if probe.active {
			break
		}
	}
	return selected
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
	out := runServiceProbeCmd("ss", "-tlnp")
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
	return format.FormatBytes(totalKB * 1024)
}

// readVmRSS reads VmRSS in kB from /proc/<pid>/status.
func readVmRSS(pid string) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%s/status", pid))
	if err != nil {
		return 0
	}
	out := string(data)
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

// toggleService starts or stops a database service
func (a *App) toggleService(info *serviceInfo) {
	if !info.installed {
		a.ShowAlert(fmt.Sprintf("%s %s is not installed.\n\nInstall it first:\n  sudo apt install %s",
			iconWarn,
			info.name, serviceInstallPackage(info.name)), "services")
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
		// Passwords are exact values. Trimming here made valid passwords with
		// leading or trailing spaces fail authentication.
		password := passwordField.GetText()
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
	loadingToken := a.showLoadingModal(fmt.Sprintf("%s %s %s...", iconServices, actionTitle, info.name),
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
			if !a.finishLoadingModal(loadingToken) {
				return
			}

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
	cancelText  string
	onCancel    func()
	waitForDone bool
}

type loadingReturnState struct {
	page  string
	focus tview.Primitive
}

type loadingModalOption func(*loadingModalOptions)

func withLoadingCancel(cancelText string, onCancel func()) loadingModalOption {
	return func(opts *loadingModalOptions) {
		opts.cancelText = cancelText
		opts.onCancel = onCancel
	}
}

// withLoadingCancelOutcome keeps the loader visible after a cancel request so
// the worker can report whether it stopped before or after its durable commit.
// Use it for operations such as backups where that distinction matters.
func withLoadingCancelOutcome(cancelText string, onCancel func()) loadingModalOption {
	return func(opts *loadingModalOptions) {
		opts.cancelText = cancelText
		opts.onCancel = onCancel
		opts.waitForDone = true
	}
}

// showLoadingModal displays a loading spinner with optional cancellation and
// returns an ownership token. Async completions must finish with that token so
// an older worker can never dismiss a newer operation's overlay.
func (a *App) showLoadingModal(message string, options ...loadingModalOption) uint64 {
	returnPage, _ := a.pages.GetFrontPage()
	returnState := loadingReturnState{page: returnPage, focus: a.app.GetFocus()}
	if returnPage == "loading" {
		// A replacement loader should ultimately return to the page beneath the
		// first loader, not to the detached modal it replaced.
		a.loadingMu.Lock()
		var latestToken uint64
		for existingToken, existingState := range a.loadingReturns {
			if existingToken > latestToken {
				latestToken = existingToken
				returnState = existingState
			}
		}
		a.loadingMu.Unlock()
	}
	token := a.loadingGeneration.Add(1)
	a.loadingMu.Lock()
	if a.loadingReturns == nil {
		a.loadingReturns = make(map[uint64]loadingReturnState)
	}
	a.loadingReturns[token] = returnState
	a.loadingMu.Unlock()
	opts := loadingModalOptions{
		cancelText: "Please wait; this step cannot be cancelled safely.",
	}
	for _, option := range options {
		option(&opts)
	}

	modalText := fmt.Sprintf("\n%s %s\n\n%s", iconRefresh, tview.Escape(message), tview.Escape(opts.cancelText))
	modal := tview.NewModal().
		SetText(modalText).
		SetBackgroundColor(bg).
		SetTextColor(text)

	if opts.onCancel != nil {
		cancel := opts.onCancel
		cancelRequested := false
		modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyCtrlC {
				if !cancelRequested {
					cancelRequested = true
					if opts.waitForDone {
						modal.SetText(fmt.Sprintf("\n%s Cancel requested\n\nWaiting for the current step to reach a safe stopping point.\n\nThe final outcome will appear here.", iconWarn))
						a.updateStatusBar("[yellow]Cancel requested; waiting for a safe stopping point.[-]", 0)
						cancel()
					} else if a.finishLoadingModal(token) {
						a.updateStatusBar("[yellow]Operation canceled.[-]", 0)
						cancel()
					}
				}
				return nil
			}
			return event
		})
	}

	a.pages.AddPage("loading", modal, true, true)
	a.app.SetFocus(modal)
	return token
}

func (a *App) finishLoadingModal(token uint64) bool {
	if a == nil || token == 0 {
		return false
	}
	a.loadingMu.Lock()
	returnState, hasReturnState := a.loadingReturns[token]
	delete(a.loadingReturns, token)
	a.loadingMu.Unlock()
	if !a.loadingGeneration.CompareAndSwap(token, token+1) {
		return false
	}
	loaderWasFront := false
	if a.pages != nil {
		frontPage, _ := a.pages.GetFrontPage()
		loaderWasFront = frontPage == "loading"
	}
	if a.pages != nil {
		a.pages.RemovePage("loading")
	}
	// A newer alert/modal may have been layered above this loader. In that
	// case remove only the underlying loading page and preserve the newer
	// primitive's keyboard focus.
	if loaderWasFront && hasReturnState {
		a.restoreLoadingReturnState(returnState)
	}
	return true
}

func (a *App) restoreLoadingReturnState(state loadingReturnState) {
	if a == nil || a.app == nil {
		return
	}
	if state.page != "" && a.pages != nil && !a.pages.HasPage(state.page) {
		state.focus = nil
	}
	if state.page == "main" {
		switch state.focus {
		case a.tables, a.queryInput, a.results:
			a.setFocusWithColor(state.focus)
			return
		}
		switch a.focusedPanel {
		case a.tables, a.queryInput, a.results:
			a.setFocusWithColor(a.focusedPanel)
			return
		}
		if a.tables != nil {
			a.setFocusWithColor(a.tables)
		}
		return
	}
	if state.focus != nil {
		a.app.SetFocus(state.focus)
	}
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

// runServiceProbeCmd is deliberately tighter than runCmd. The Services page
// runs several local-only probes and should remain responsive even when
// systemd is unavailable or a client command is unhealthy. Preserve stdout
// from non-zero commands because systemctl reports useful inactive states that
// way.
func runServiceProbeCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, _ := cmd.Output()
	if ctx.Err() != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getQuickStatus returns a short colored status string for the dashboard header
func getQuickStatus(unitName string) string {
	probe := probeSystemdUnits(getUnitNames(unitName))
	name := strings.ToUpper(unitName[:1]) + unitName[1:]
	if probe.active {
		var color string
		switch unitName {
		case "mysql":
			color = "#f9e2af"
		case "postgresql":
			color = "#89b4fa"
		}
		return fmt.Sprintf("[green]●[-] [%s]%s[-]", color, name)
	}
	if probe.loaded {
		return fmt.Sprintf("[red]○[-] [#6c7086]%s[-]", name)
	}
	return fmt.Sprintf("[#6c7086]○ %s (n/a)[-]", name)
}

const (
	serviceConnectionPage  = "connectService"
	serviceDatabasePicker  = "serviceDatabasePicker"
	serviceLabelSavedLogin = "Saved login"
	serviceLabelType       = "Service"
)

func localServiceLogins(store *config.Store) []config.ConnectionConfig {
	if store == nil {
		return nil
	}
	logins := make([]config.ConnectionConfig, 0, len(store.Connections))
	for _, connection := range store.Connections {
		if connection.Type != config.MySQL && connection.Type != config.PostgreSQL {
			continue
		}
		if !isLocalDatabaseHost(connection.Host) {
			continue
		}
		logins = append(logins, connection)
	}
	return logins
}

func isLocalDatabaseHost(host string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	return host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func serviceLoginLabel(connection config.ConnectionConfig) string {
	host := strings.TrimSpace(connection.Host)
	if host == "" {
		host = "localhost"
	}
	databaseName := strings.TrimSpace(connection.Database)
	if databaseName == "" {
		databaseName = "server"
	}
	return fmt.Sprintf("%s · %s@%s/%s", connection.TypeLabel(), connection.User, host, databaseName)
}

func buildServiceConnectionConfig(form *tview.Form) (*config.ConnectionConfig, error) {
	if form == nil {
		return nil, fmt.Errorf("connection form is unavailable")
	}
	dropdown, ok := form.GetFormItemByLabel(serviceLabelType).(*tview.DropDown)
	if !ok {
		return nil, fmt.Errorf("could not read the selected service")
	}
	_, serviceName := dropdown.GetCurrentOption()

	cfg := &config.ConnectionConfig{
		Host:     formInputValueByLabel(form, connLabelHost),
		Port:     formInputValueByLabel(form, connLabelPort),
		User:     formInputValueByLabel(form, connLabelUser),
		Database: formInputValueByLabel(form, connLabelDatabase),
	}
	if passwordField, ok := form.GetFormItemByLabel(connLabelPassword).(*tview.InputField); ok {
		// A password is not prose: preserve whitespace exactly as entered.
		cfg.Password = passwordField.GetText()
	}

	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("user is required")
	}
	switch serviceName {
	case "PostgreSQL":
		cfg.Type = config.PostgreSQL
		cfg.SSLMode = "disable"
		if cfg.Port == "" {
			cfg.Port = "5432"
		}
	case "MySQL":
		cfg.Type = config.MySQL
		if cfg.Port == "" {
			cfg.Port = "3306"
		}
	default:
		return nil, fmt.Errorf("unsupported service %q", serviceName)
	}

	if cfg.Database == "" {
		cfg.Name = cfg.TypeLabel() + " server"
	} else {
		cfg.Name = cfg.Database
	}
	return cfg, nil
}

// showConnectServiceModal displays a focused local-service connection flow.
// Discovery and connection both use the values visible in this form.
func (a *App) showConnectServiceModal() {
	form := tview.NewForm()
	form.SetTitle(fmt.Sprintf(" %s Connect to a Local Service ", iconConnect))
	form.SetTitleColor(blue)
	form.SetBorder(true)
	form.SetBorderColor(blue)

	savedLogins := localServiceLogins(a.store)
	savedOptions := []string{"Manual credentials"}
	selectedSavedLogin := 0
	for index, connection := range savedLogins {
		savedOptions = append(savedOptions, serviceLoginLabel(connection))
		if connection.Active || (a.activeConn != nil && a.activeConn.ID != "" && connection.ID == a.activeConn.ID) {
			selectedSavedLogin = index + 1
		}
	}

	form.AddDropDown(serviceLabelSavedLogin, savedOptions, 0, nil)
	form.AddDropDown(serviceLabelType, []string{"MySQL", "PostgreSQL"}, 0, nil)
	form.AddInputField(connLabelHost, "localhost", 30, nil, nil)
	form.AddInputField(connLabelPort, "3306", 10, nil, nil)
	form.AddInputField(connLabelDatabase, "", 30, nil, nil)
	form.AddInputField(connLabelUser, "root", 30, nil, nil)
	form.AddPasswordField(connLabelPassword, "", 30, '*', nil)

	savedDropDown := form.GetFormItemByLabel(serviceLabelSavedLogin).(*tview.DropDown)
	savedDropDown.SetUseStyleTags(false)
	serviceDropDown := form.GetFormItemByLabel(serviceLabelType).(*tview.DropDown)
	applyingSavedLogin := false
	serviceDropDown.SetSelectedFunc(func(serviceName string, _ int) {
		if !applyingSavedLogin {
			savedDropDown.SetCurrentOption(0)
		}
		userInput := form.GetFormItemByLabel(connLabelUser).(*tview.InputField)
		portInput := form.GetFormItemByLabel(connLabelPort).(*tview.InputField)
		if serviceName == "PostgreSQL" {
			userInput.SetText("postgres")
			portInput.SetText("5432")
		} else {
			userInput.SetText("root")
			portInput.SetText("3306")
		}
	})
	savedDropDown.SetSelectedFunc(func(_ string, optionIndex int) {
		if optionIndex <= 0 || optionIndex > len(savedLogins) {
			return
		}
		connection := savedLogins[optionIndex-1]
		applyingSavedLogin = true
		if connection.Type == config.PostgreSQL {
			serviceDropDown.SetCurrentOption(1)
		} else {
			serviceDropDown.SetCurrentOption(0)
		}
		setFormInputValueByLabel(form, connLabelHost, connection.Host)
		setFormInputValueByLabel(form, connLabelPort, connection.Port)
		setFormInputValueByLabel(form, connLabelDatabase, connection.Database)
		setFormInputValueByLabel(form, connLabelUser, connection.User)
		setFormInputValueByLabel(form, connLabelPassword, connection.Password)
		applyingSavedLogin = false
	})
	if selectedSavedLogin > 0 {
		savedDropDown.SetCurrentOption(selectedSavedLogin)
	}

	form.AddButton("Connect / Browse", func() {
		cfg, err := buildServiceConnectionConfig(form)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Cannot connect:\n\n%v", iconInfo, err), serviceConnectionPage)
			return
		}
		if strings.TrimSpace(cfg.Database) == "" {
			a.discoverServiceDatabases(form)
			return
		}
		a.connectServiceConfig(cfg)
	})

	form.AddButton("Find DBs", func() {
		a.discoverServiceDatabases(form)
	})

	closeForm := func() {
		a.pages.RemovePage(serviceConnectionPage)
		a.pages.ShowPage("services")
	}
	form.AddButton("Cancel", closeForm)

	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle)
	form.SetButtonBackgroundColor(surface1)
	form.SetButtonTextColor(green)
	form.SetLabelColor(text)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closeForm()
			return nil
		}
		return event
	})

	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(" [yellow]Database is optional[-] — Connect lists every database visible to this DB login  │  [yellow]Esc[-] Back %s", iconBack))
	hint.SetBackgroundColor(crust)
	formWithHint := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(hint, 1, 0, false)

	modalW, modalH := a.modalSize(68, 92, 20, 24)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(formWithHint, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(serviceConnectionPage, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) connectServiceConfig(cfg *config.ConnectionConfig) {
	loadingToken := a.showLoadingModal(fmt.Sprintf("Connecting to %s...", cfg.Database))
	selectedTable := a.selectedTable
	currentTableIndex := a.tables.GetCurrentItem()

	go func() {
		db, connectErr := database.Connect(cfg)
		var snapshot *tableListSnapshot
		var tableLoadErr error
		if connectErr == nil {
			snapshot, tableLoadErr = loadTableListSnapshot(db, cfg.Type, selectedTable, currentTableIndex)
		}
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				if db != nil {
					_ = db.Close()
				}
				return
			}
			if connectErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Connection failed:\n\n%v\n\n%s",
					iconFail, connectErr, connectionHint(connectErr, cfg)), serviceConnectionPage)
				return
			}

			a.cleanup()
			a.db = db
			a.dbType = cfg.Type
			a.dbName = cfg.Name
			a.activeConn = cloneConnectionConfig(cfg)
			if tableLoadErr != nil {
				a.applyTableListSnapshot(&tableListSnapshot{
					items: []tableListSnapshotItem{{label: fmt.Sprintf("[gray]%s Tables could not be loaded[-]", iconWarn)}},
				})
				a.ShowAlert(fmt.Sprintf("%s Connected, but could not load tables:\n\n%v\n\nYou can still run queries manually.", iconWarn, tableLoadErr), "main")
			} else {
				a.applyTableListSnapshot(snapshot)
				a.loadDatabaseObjects()
			}
			a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults))
			a.pages.RemovePage(serviceConnectionPage)
			a.pages.RemovePage("services")
			a.pages.ShowPage("main")
			a.setFocusWithColor(a.tables)
			a.updateStatusBar(fmt.Sprintf("[green]Connected to %s[-]", cfg.Database), 0)
		})
	}()
}

func (a *App) discoverServiceDatabases(form *tview.Form) {
	cfg, err := buildServiceConnectionConfig(form)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Cannot find databases:\n\n%v", iconInfo, err), serviceConnectionPage)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	loadingToken := a.showLoadingModal(
		fmt.Sprintf("Finding databases on %s:%s...", cfg.Host, cfg.Port),
		withLoadingCancel("Press Esc to cancel database discovery.", cancel),
	)

	go func() {
		defer cancel()
		names, discoveryErr := discoverDatabaseNames(ctx, cfg)
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			if errors.Is(discoveryErr, context.Canceled) {
				return
			}
			if discoveryErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not find databases on %s:%s:\n\n%v\n\n%s",
					iconWarn, cfg.Host, cfg.Port, discoveryErr, connectionHint(discoveryErr, cfg)), serviceConnectionPage)
				return
			}
			if len(names) == 0 {
				a.ShowAlert(fmt.Sprintf("%s No user databases are visible to %q on %s:%s.",
					iconInfo, cfg.User, cfg.Host, cfg.Port), serviceConnectionPage)
				return
			}
			names = databasesWithCurrentFirst(names, formInputValueByLabel(form, connLabelDatabase))
			a.showServiceDatabasePicker(form, cfg, names)
		})
	}()
}

func (a *App) showServiceDatabasePicker(parentForm *tview.Form, cfg *config.ConnectionConfig, names []string) {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Open Database (%s) ", iconDatabase, cfg.TypeLabel())).
		SetBorderColor(blue).
		SetTitleColor(mauve).
		SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	returnToForm := func() {
		a.pages.RemovePage(serviceDatabasePicker)
		if databaseIndex := parentForm.GetFormItemIndex(connLabelDatabase); databaseIndex >= 0 {
			parentForm.SetFocus(databaseIndex)
		}
		a.app.SetFocus(parentForm)
	}

	for _, name := range names {
		databaseName := name
		list.AddItem(fmt.Sprintf("  %s  %s", iconDatabase, tview.Escape(databaseName)), "", 0, func() {
			setFormInputValueByLabel(parentForm, connLabelDatabase, databaseName)
			a.pages.RemovePage(serviceDatabasePicker)
			selected := connectionForDatabase(*cfg, databaseName)
			a.connectServiceConfig(&selected)
		})
	}

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			returnToForm()
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(" [yellow]↑/↓[-] Choose  │  [yellow]Enter[-] Open database  │  [yellow]Esc[-] Back %s  │  [#6c7086]%d accessible[-]", iconBack, len(names)))
	footer.SetBackgroundColor(crust)

	picker := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(44, 68, 10, 24)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(picker, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(serviceDatabasePicker, grid, true, true)
	a.app.SetFocus(list)
}
