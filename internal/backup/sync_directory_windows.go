//go:build windows

package backup

// Windows directory handles do not provide portable fsync semantics. File
// replacement uses MOVEFILE_WRITE_THROUGH, and completed artifact files are
// flushed before publication.
func syncDirectory(string) error { return nil }
