// Package storeinstall identifies Store-managed Windows installations.
package storeinstall

import "strings"

// IsPackagePath also recognizes development registrations staged outside WindowsApps
// when the caller uses Managed, which asks Windows for the current package identity.
func IsPackagePath(path string) bool {
	path = strings.ToLower(strings.ReplaceAll(path, "/", "\\"))
	return strings.Contains(path, `\windowsapps\shreyam1008.dbterm_`)
}
