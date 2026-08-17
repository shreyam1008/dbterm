package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/persist"
)

const (
	pageUpdates             = "updates"
	defaultUpdateRepository = "shreyam1008/dbterm"
)

var updateRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func validUpdateRepository(repository string) bool {
	if !updateRepositoryPattern.MatchString(repository) {
		return false
	}
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && parts[0] != "." && parts[0] != ".." && parts[1] != "." && parts[1] != ".."
}

type latestRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

func normalizeBuildInfo(info BuildInfo) BuildInfo {
	info.Version = strings.TrimPrefix(strings.TrimSpace(info.Version), "v")
	if info.Version == "" {
		info.Version = "dev"
	}
	info.ReleaseName = strings.TrimSpace(info.ReleaseName)
	info.Commit = strings.TrimSpace(info.Commit)
	if info.Commit == "" {
		info.Commit = "dev"
	}
	info.Repository = strings.TrimSpace(info.Repository)
	if !validUpdateRepository(info.Repository) {
		info.Repository = defaultUpdateRepository
	}
	return info
}

func (a *App) showUpdates() {
	if a == nil || a.app == nil || a.pages == nil {
		return
	}
	returnPage, _ := a.pages.GetFrontPage()
	returnFocus := a.app.GetFocus()

	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText("\n[::b][#cba6f7]dbterm  ·  Version & Update[-][-]\n[#a6adc8]Check releases and install without leaving the app[-]")

	details := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetWordWrap(true).
		SetScrollable(true)
	details.SetBorder(true).
		SetTitle(" Release status ").
		SetTitleColor(mauve).
		SetBorderColor(surface1).
		SetBackgroundColor(bg)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Tab[-] Move  │  [yellow]Enter[-] Select  │  [yellow]Esc[-] Back ")
	footer.SetBackgroundColor(crust)

	var release *latestRelease
	var checkErr error
	checking := false
	updatedTo := ""
	connectionPath, _ := persist.DefaultConfigFile("connections.json")
	if strings.TrimSpace(connectionPath) == "" {
		connectionPath = "OS-native dbterm config directory/connections.json"
	}
	recoveryVault := "OS-native dbterm state directory/profile-recovery/connections.json"
	if stateDir, err := appdirs.StateDir(); err == nil {
		recoveryVault = filepath.Join(stateDir, "profile-recovery", "connections.json")
	}
	var layout *tview.Flex

	render := func() {
		currentName := ""
		if a.buildInfo.ReleaseName != "" {
			currentName = fmt.Sprintf("  [#f9e2af]%s[-]", tview.Escape(a.buildInfo.ReleaseName))
		}
		var status string
		switch {
		case checking:
			status = "[#89b4fa]Checking GitHub Releases…[-]"
		case checkErr != nil:
			status = fmt.Sprintf("[#f38ba8]Could not check the latest release:[-] %s", tview.Escape(checkErr.Error()))
		case release == nil:
			status = "[#6c7086]Latest release has not been checked yet.[-]"
		default:
			latestVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
			latestName := strings.TrimSpace(release.Name)
			if latestName == "" || strings.EqualFold(latestName, release.TagName) {
				latestName = "release"
			}
			if updateAvailable(a.buildInfo.Version, latestVersion) {
				status = fmt.Sprintf("[#f9e2af]Update available:[-] [::b]v%s[-]  [#a6e3a1]%s[-]", tview.Escape(latestVersion), tview.Escape(latestName))
			} else {
				status = fmt.Sprintf("[#a6e3a1]You are up to date.[-]  Latest: v%s  %s", tview.Escape(latestVersion), tview.Escape(latestName))
			}
			if published := formatReleaseDate(release.PublishedAt); published != "" {
				status += "\n[#6c7086]Published " + tview.Escape(published) + "[-]"
			}
			if notes := compactReleaseNotes(release.Body, 700); notes != "" {
				status += "\n\n[#a6adc8]Release notes[-]\n" + tview.Escape(notes)
			}
		}
		if updatedTo != "" {
			status = fmt.Sprintf("[#a6e3a1]Installed v%s successfully.[-]\nQuit and reopen dbterm to run the new version.\n\n%s", tview.Escape(updatedTo), status)
		}
		details.SetText(fmt.Sprintf(
			"\n  [#a6adc8]Current version[-]   [::b]v%s[-]%s\n  [#a6adc8]Build commit[-]     %s\n  [#a6adc8]Connection profile[-] %s\n  [#a6adc8]Local recovery[-]   %s.bak  +  previous generation\n  [#a6adc8]State vault[-]      %s  +  previous generation\n\n  %s\n",
			tview.Escape(a.buildInfo.Version), currentName, tview.Escape(a.buildInfo.Commit),
			tview.Escape(connectionPath), tview.Escape(connectionPath), tview.Escape(recoveryVault), status,
		))
		details.ScrollToBeginning()
	}

	checkLatest := func() {
		if checking {
			return
		}
		checking = true
		checkErr = nil
		render()
		go func(repository string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			found, err := fetchLatestRelease(ctx, http.DefaultClient, repository)
			a.app.QueueUpdateDraw(func() {
				if a.pages.GetPage(pageUpdates) != layout {
					return
				}
				checking = false
				checkErr = err
				if err == nil {
					release = &found
				}
				render()
			})
		}(a.buildInfo.Repository)
	}

	back := func() {
		a.pages.RemovePage(pageUpdates)
		if returnPage != "" && a.pages.HasPage(returnPage) {
			a.pages.ShowPage(returnPage).SendToFront(returnPage)
		}
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
	}

	installLatest := func() {
		if checking {
			a.ShowAlert(fmt.Sprintf("%s Still checking the latest release.", iconInfo), pageUpdates)
			return
		}
		if checkErr != nil || release == nil {
			a.ShowAlert(fmt.Sprintf("%s Check for updates first.", iconInfo), pageUpdates)
			return
		}
		version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		if !updateAvailable(a.buildInfo.Version, version) {
			a.ShowAlert(fmt.Sprintf("%s dbterm v%s is already current.", iconSuccess, tview.Escape(a.buildInfo.Version)), pageUpdates)
			return
		}
		a.confirmInAppUpdate(version, func() {
			if err := a.runInAppUpdate(version); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Update failed:\n\n%s\n\nYour current installation and profile were left in place.", iconWarn, tview.Escape(err.Error())), pageUpdates)
				return
			}
			updatedTo = version
			render()
			a.ShowAlert(fmt.Sprintf("%s dbterm v%s was installed.\n\nQuit and reopen dbterm to use it. Saved connections and state were not replaced.", iconSuccess, tview.Escape(version)), pageUpdates)
		})
	}

	actions := tview.NewForm().
		AddButton("Check Again", checkLatest).
		AddButton("Install Update", installLatest).
		AddButton("Back", back)
	actions.SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetBackgroundColor(bg)
	actions.SetBorder(false)
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			back()
			return nil
		}
		return event
	})
	details.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			back()
			return nil
		}
		return event
	})

	layout = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 5, 0, false).
		AddItem(details, 0, 1, false).
		AddItem(actions, 3, 0, true).
		AddItem(footer, 1, 0, false)

	render()
	a.pages.AddAndSwitchToPage(pageUpdates, layout, true)
	a.app.SetFocus(actions)
	checkLatest()
}

func (a *App) confirmInAppUpdate(version string, install func()) {
	const pageConfirmUpdate = "confirmUpdate"
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Install dbterm v%s now?\n\nThe published release asset is downloaded and its SHA-256 checksum is verified before only the executable is replaced. Your profile is guarded separately.", tview.Escape(version))).
		AddButtons([]string{"  Install  ", "  Cancel  "}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			a.pages.RemovePage(pageConfirmUpdate)
			if buttonIndex == 0 {
				install()
				return
			}
			a.pages.ShowPage(pageUpdates)
		})
	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)
	a.pages.AddPage(pageConfirmUpdate, modal, true, true)
}

func (a *App) runInAppUpdate(version string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	command, args, elevated, err := inAppUpdateCommand(executable, version)
	if err != nil {
		return err
	}
	var runErr error
	suspended := a.app.Suspend(func() {
		fmt.Println()
		if elevated {
			fmt.Println("dbterm needs administrator permission to replace the installed executable.")
		}
		cmd := exec.Command(command, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		runErr = cmd.Run()
	})
	if !suspended {
		return fmt.Errorf("could not temporarily leave terminal UI mode")
	}
	if runErr != nil {
		return fmt.Errorf("updater exited unsuccessfully: %w", runErr)
	}
	return nil
}

func fetchLatestRelease(ctx context.Context, client *http.Client, repository string) (latestRelease, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if !validUpdateRepository(repository) {
		return latestRelease{}, fmt.Errorf("invalid release repository %q", repository)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return latestRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "dbterm-update-check")
	response, err := client.Do(request)
	if err != nil {
		return latestRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return latestRelease{}, fmt.Errorf("GitHub Releases returned HTTP %d", response.StatusCode)
	}
	var release latestRelease
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&release); err != nil {
		return latestRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return latestRelease{}, fmt.Errorf("latest release did not include a version tag")
	}
	return release, nil
}

func updateAvailable(current, latest string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	latest = strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if latest == "" {
		return false
	}
	if current == "" || strings.EqualFold(current, "dev") {
		return true
	}
	comparison, ok := compareSemver(current, latest)
	if !ok {
		return !strings.EqualFold(current, latest)
	}
	return comparison < 0
}

func compareSemver(left, right string) (int, bool) {
	parse := func(value string) ([3]int, bool) {
		var parsed [3]int
		core := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(value), "v"), "-", 2)[0]
		parts := strings.Split(core, ".")
		if len(parts) != 3 {
			return parsed, false
		}
		for index, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number < 0 {
				return parsed, false
			}
			parsed[index] = number
		}
		return parsed, true
	}
	l, lok := parse(left)
	r, rok := parse(right)
	if !lok || !rok {
		return 0, false
	}
	for index := range l {
		if l[index] < r[index] {
			return -1, true
		}
		if l[index] > r[index] {
			return 1, true
		}
	}
	return 0, true
}

func compactReleaseNotes(notes string, limit int) string {
	notes = strings.Join(strings.Fields(strings.TrimSpace(notes)), " ")
	if limit <= 0 || len([]rune(notes)) <= limit {
		return notes
	}
	runes := []rune(notes)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func formatReleaseDate(value string) string {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Local().Format("2 Jan 2006, 15:04")
}
