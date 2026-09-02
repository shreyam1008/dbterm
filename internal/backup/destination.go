package backup

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

const RcloneDestinationPrefix = "rclone://"

type destinationKind uint8

const (
	destinationLocal destinationKind = iota
	destinationRclone
)

// destinationSpec is the normalized storage location for a backup job. Local
// paths use the operating-system filesystem. rclone paths deliberately keep
// rclone's configured remote name separate from its object path so credentials
// never need to be embedded in a job.
type destinationSpec struct {
	kind       destinationKind
	localPath  string
	remoteName string
	remotePath string
}

func parseDestination(raw string) (destinationSpec, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return destinationSpec{}, fmt.Errorf("backup destination is required")
	}
	if strings.HasPrefix(strings.ToLower(value), RcloneDestinationPrefix) {
		return parseRcloneDestination(value)
	}
	resolved, err := resolveDestination(value)
	if err != nil {
		return destinationSpec{}, err
	}
	return destinationSpec{kind: destinationLocal, localPath: resolved}, nil
}

func parseRcloneDestination(raw string) (destinationSpec, error) {
	remainder := strings.TrimSpace(raw[len(RcloneDestinationPrefix):])
	remoteName, remotePath, found := strings.Cut(remainder, "/")
	if !found {
		remotePath = ""
	}
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		return destinationSpec{}, fmt.Errorf("rclone destination requires a configured remote name, for example rclone://offsite/dbterm")
	}
	for _, char := range remoteName {
		if unicode.IsControl(char) || unicode.IsSpace(char) || strings.ContainsRune(`:/\\?#@`, char) {
			return destinationSpec{}, fmt.Errorf("invalid rclone remote name %q", remoteName)
		}
	}
	remotePath = strings.TrimSpace(remotePath)
	if strings.ContainsRune(remotePath, '\\') {
		return destinationSpec{}, fmt.Errorf("rclone destination paths must use forward slashes")
	}
	if strings.ContainsAny(remotePath, "?#") {
		return destinationSpec{}, fmt.Errorf("rclone destination must not contain a query or fragment")
	}
	for _, segment := range strings.Split(remotePath, "/") {
		if segment == ".." {
			return destinationSpec{}, fmt.Errorf("rclone destination must not contain parent path segments")
		}
	}
	remotePath = strings.TrimPrefix(path.Clean("/"+remotePath), "/")
	if remotePath == "." {
		remotePath = ""
	}
	return destinationSpec{kind: destinationRclone, remoteName: remoteName, remotePath: remotePath}, nil
}

func (destination destinationSpec) String() string {
	if destination.kind == destinationRclone {
		if destination.remotePath == "" {
			return RcloneDestinationPrefix + destination.remoteName
		}
		return RcloneDestinationPrefix + destination.remoteName + "/" + destination.remotePath
	}
	return destination.localPath
}

func (destination destinationSpec) rclonePath() string {
	return destination.remoteName + ":" + destination.remotePath
}

func (destination destinationSpec) join(filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\\`) {
		return "", fmt.Errorf("backup file name must be a single name")
	}
	if destination.kind == destinationRclone {
		joined := destination
		joined.remotePath = path.Join(destination.remotePath, filename)
		return joined.String(), nil
	}
	return filepath.Join(destination.localPath, filename), nil
}

func (destination destinationSpec) parentAndName() (destinationSpec, string, error) {
	if destination.kind == destinationRclone {
		name := path.Base(destination.remotePath)
		if destination.remotePath == "" || name == "." || name == "/" {
			return destinationSpec{}, "", fmt.Errorf("remote backup output must include a file name")
		}
		parent := destination
		parent.remotePath = path.Dir(destination.remotePath)
		if parent.remotePath == "." {
			parent.remotePath = ""
		}
		return parent, name, nil
	}
	name := filepath.Base(destination.localPath)
	if name == "." || name == string(filepath.Separator) {
		return destinationSpec{}, "", fmt.Errorf("backup output must include a file name")
	}
	return destinationSpec{kind: destinationLocal, localPath: filepath.Dir(destination.localPath)}, name, nil
}

// NormalizeBackupDestination returns a stable local or legacy-rclone storage
// value. Job.Validate separately rejects rclone for new backup generation;
// parsing remains available for historical records and the future copy layer.
func NormalizeBackupDestination(raw string) (string, error) {
	destination, err := parseDestination(raw)
	if err != nil {
		return "", err
	}
	return destination.String(), nil
}

// IsRemoteBackupDestination reports whether value uses dbterm's first-class
// rclone destination syntax. Invalid rclone values still return true so callers
// never accidentally reinterpret them as local filesystem paths.
func IsRemoteBackupDestination(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), RcloneDestinationPrefix)
}

// JoinBackupDestination appends one artifact name without converting a legacy
// rclone URI into a local path. Generation policy is enforced by Job.Validate.
func JoinBackupDestination(raw, filename string) (string, error) {
	destination, err := parseDestination(raw)
	if err != nil {
		return "", err
	}
	return destination.join(filename)
}

func ensureDestinationContext(ctx context.Context, destination destinationSpec) error {
	if destination.kind == destinationRclone {
		return ensureRcloneDestination(ctx, destination)
	}
	return ensureDestination(destination.localPath)
}
