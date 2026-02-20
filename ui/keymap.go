package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/config"
)

type keymapAction string

const (
	actionFocusTables    keymapAction = config.ActionFocusTables
	actionFocusQuery     keymapAction = config.ActionFocusQuery
	actionFocusResults   keymapAction = config.ActionFocusResults
	actionDashboard      keymapAction = config.ActionDashboard
	actionHelp           keymapAction = config.ActionHelp
	actionServices       keymapAction = config.ActionServices
	actionFullscreen     keymapAction = config.ActionFullscreen
	actionBackup         keymapAction = config.ActionBackup
	actionExportCSV      keymapAction = config.ActionExportCSV
	actionHistory        keymapAction = config.ActionHistory
	actionSettings       keymapAction = config.ActionSettings
	actionImportDump     keymapAction = config.ActionImportDump
	actionSelectAll      keymapAction = config.ActionSelectAll
	actionClearSelection keymapAction = config.ActionClearSelection
)

var knownKeymapActions = map[keymapAction]struct{}{
	actionFocusTables:    {},
	actionFocusQuery:     {},
	actionFocusResults:   {},
	actionDashboard:      {},
	actionHelp:           {},
	actionServices:       {},
	actionFullscreen:     {},
	actionBackup:         {},
	actionExportCSV:      {},
	actionHistory:        {},
	actionSettings:       {},
	actionImportDump:     {},
	actionSelectAll:      {},
	actionClearSelection: {},
}

type actionKeymap struct {
	actionByBinding map[string]keymapAction
}

type keyModifiers struct {
	ctrl  bool
	alt   bool
	shift bool
}

func newActionKeymap(settings *config.Settings) (*actionKeymap, error) {
	bindingsByAction, err := defaultActionBindings()
	if err != nil {
		return nil, err
	}

	if settings != nil {
		for rawAction, rawBindings := range settings.Keymap {
			action, ok := normalizeActionName(rawAction)
			if !ok {
				return nil, fmt.Errorf("unknown keymap action %q (known: %s)", rawAction, strings.Join(knownActionNames(), ", "))
			}

			bindings := cleanBindings(rawBindings)
			if len(bindings) == 0 {
				continue
			}
			bindingsByAction[action] = bindings
		}
	}

	resolver := &actionKeymap{
		actionByBinding: make(map[string]keymapAction, len(bindingsByAction)),
	}

	for action, bindings := range bindingsByAction {
		seen := map[string]struct{}{}
		for _, rawBinding := range bindings {
			normalized, err := normalizeBinding(rawBinding)
			if err != nil {
				return nil, fmt.Errorf("invalid key binding %q for action %q: %w", rawBinding, action, err)
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}

			if existing, exists := resolver.actionByBinding[normalized]; exists && existing != action {
				return nil, fmt.Errorf("duplicate key binding %q for actions %q and %q", normalized, existing, action)
			}

			resolver.actionByBinding[normalized] = action
		}
	}

	return resolver, nil
}

func defaultActionBindings() (map[keymapAction][]string, error) {
	defaults := make(map[keymapAction][]string, len(knownKeymapActions))

	for rawAction, rawBindings := range config.DefaultKeymapBindings() {
		action, ok := normalizeActionName(rawAction)
		if !ok {
			return nil, fmt.Errorf("default keymap contains unknown action %q", rawAction)
		}
		defaults[action] = cleanBindings(rawBindings)
	}

	for action := range knownKeymapActions {
		if _, ok := defaults[action]; !ok {
			return nil, fmt.Errorf("missing default key binding for action %q", action)
		}
	}

	return defaults, nil
}

func (k *actionKeymap) Resolve(event *tcell.EventKey) (keymapAction, bool) {
	if k == nil || event == nil || len(k.actionByBinding) == 0 {
		return "", false
	}

	normalized, ok := normalizeEvent(event)
	if !ok {
		return "", false
	}

	action, exists := k.actionByBinding[normalized]
	return action, exists
}

func normalizeActionName(raw string) (keymapAction, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return "", false
	}

	action := keymapAction(name)
	_, ok := knownKeymapActions[action]
	return action, ok
}

func knownActionNames() []string {
	names := make([]string, 0, len(knownKeymapActions))
	for action := range knownKeymapActions {
		names = append(names, string(action))
	}
	sort.Strings(names)
	return names
}

func normalizeBinding(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.ReplaceAll(value, " ", "")
	value = normalizeModifierSeparators(value)
	if value == "" {
		return "", fmt.Errorf("binding is empty")
	}

	parts := strings.Split(value, "+")
	mods := keyModifiers{}
	key := ""
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}

		if applyModifierToken(token, &mods) {
			continue
		}

		if key != "" {
			return "", fmt.Errorf("multiple keys in binding")
		}

		normalizedToken, err := normalizeKeyToken(token)
		if err != nil {
			return "", err
		}
		key = normalizedToken
	}

	if key == "" {
		return "", fmt.Errorf("missing key token")
	}

	return composeBinding(mods, key), nil
}

func normalizeModifierSeparators(value string) string {
	replacer := strings.NewReplacer(
		"ctrl-", "ctrl+",
		"control-", "control+",
		"ctl-", "ctl+",
		"cmd-", "cmd+",
		"command-", "command+",
		"alt-", "alt+",
		"option-", "option+",
		"meta-", "meta+",
		"shift-", "shift+",
	)
	return replacer.Replace(value)
}

func applyModifierToken(token string, mods *keyModifiers) bool {
	if mods == nil {
		return false
	}

	switch token {
	case "ctrl", "control", "ctl", "cmd", "command":
		mods.ctrl = true
		return true
	case "alt", "option", "meta":
		mods.alt = true
		return true
	case "shift":
		mods.shift = true
		return true
	default:
		return false
	}
}

func normalizeKeyToken(token string) (string, error) {
	switch token {
	case "esc", "escape":
		return "esc", nil
	case "enter", "return":
		return "enter", nil
	case "tab":
		return "tab", nil
	case "backspace", "backspace2", "bksp":
		return "backspace", nil
	case "space":
		return "space", nil
	case "plus":
		return "+", nil
	case "minus", "hyphen":
		return "-", nil
	case "underscore":
		return "_", nil
	case "comma":
		return ",", nil
	case "period", "dot":
		return ".", nil
	}

	if strings.HasPrefix(token, "f") && len(token) > 1 {
		num, err := strconv.Atoi(token[1:])
		if err == nil && num >= 1 && num <= 24 {
			return fmt.Sprintf("f%d", num), nil
		}
	}

	runes := []rune(token)
	if len(runes) == 1 {
		r := runes[0]
		if unicode.IsLetter(r) {
			return string(unicode.ToLower(r)), nil
		}
		return string(r), nil
	}

	return "", fmt.Errorf("unknown key token %q", token)
}

func normalizeEvent(event *tcell.EventKey) (string, bool) {
	if event == nil {
		return "", false
	}

	mods := keyModifiers{
		ctrl:  event.Modifiers()&tcell.ModCtrl != 0,
		alt:   event.Modifiers()&tcell.ModAlt != 0,
		shift: event.Modifiers()&tcell.ModShift != 0,
	}

	if event.Key() == tcell.KeyRune {
		key, ok := normalizeRune(event.Rune())
		if !ok {
			return "", false
		}
		// Rune already captures shifted glyphs; keeping shift breaks expected matches
		// for bindings like Alt+Q where input may produce an uppercase rune.
		mods.shift = false
		return composeBinding(mods, key), true
	}

	key := ""
	switch event.Key() {
	case tcell.KeyEscape:
		key = "esc"
	case tcell.KeyEnter:
		key = "enter"
	case tcell.KeyTab:
		key = "tab"
	case tcell.KeyBacktab:
		key = "tab"
		mods.shift = true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		key = "backspace"
	case tcell.KeyCtrlSpace:
		key = "space"
		mods.ctrl = true
	case tcell.KeyCtrlUnderscore:
		key = "_"
		mods.ctrl = true
	default:
		switch {
		case event.Key() >= tcell.KeyF1 && event.Key() <= tcell.KeyF24:
			key = fmt.Sprintf("f%d", int(event.Key()-tcell.KeyF1)+1)
		case event.Key() >= tcell.KeyCtrlA && event.Key() <= tcell.KeyCtrlZ:
			key = string(rune('a' + int(event.Key()-tcell.KeyCtrlA)))
			mods.ctrl = true
		default:
			name, ok := tcell.KeyNames[event.Key()]
			if !ok {
				return "", false
			}
			normalized, err := normalizeBinding(name)
			if err != nil {
				return "", false
			}
			merged, ok := mergeBindingAndModifiers(normalized, mods)
			return merged, ok
		}
	}

	if key == "" {
		return "", false
	}
	return composeBinding(mods, key), true
}

func mergeBindingAndModifiers(binding string, extra keyModifiers) (string, bool) {
	parts := strings.Split(binding, "+")
	if len(parts) == 0 {
		return "", false
	}

	mods := keyModifiers{}
	key := ""
	for _, part := range parts {
		if applyModifierToken(part, &mods) {
			continue
		}
		if key == "" {
			key = part
		}
	}

	if key == "" {
		return "", false
	}

	mods.ctrl = mods.ctrl || extra.ctrl
	mods.alt = mods.alt || extra.alt
	mods.shift = mods.shift || extra.shift

	return composeBinding(mods, key), true
}

func normalizeRune(r rune) (string, bool) {
	if r == 0 {
		return "", false
	}
	if unicode.IsLetter(r) {
		return string(unicode.ToLower(r)), true
	}
	if unicode.IsSpace(r) {
		return "space", true
	}
	return string(r), true
}

func composeBinding(mods keyModifiers, key string) string {
	parts := make([]string, 0, 4)
	if mods.ctrl {
		parts = append(parts, "ctrl")
	}
	if mods.alt {
		parts = append(parts, "alt")
	}
	if mods.shift {
		parts = append(parts, "shift")
	}
	parts = append(parts, key)
	return strings.Join(parts, "+")
}

func cleanBindings(bindings []string) []string {
	if len(bindings) == 0 {
		return nil
	}

	cleaned := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		item := strings.TrimSpace(binding)
		if item == "" {
			continue
		}
		cleaned = append(cleaned, item)
	}
	return cleaned
}
