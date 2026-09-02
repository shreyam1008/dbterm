package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// CopyVolumeMode describes who owns the destination mount lifecycle. The two
// verify-only modes never mount or unmount anything. Managed Linux volumes are
// the sole mode permitted to invoke mount-management commands.
type CopyVolumeMode string

const (
	CopyVolumeAlreadyMounted          CopyVolumeMode = "already_mounted"
	CopyVolumeOSManaged               CopyVolumeMode = "os_managed"
	CopyVolumeManagedLinuxBlockDevice CopyVolumeMode = "managed_linux_block_device"
)

const defaultCopyVolumeSentinelFile = ".dbterm-volume-id"

// CopyDestinationVolume binds a local copy destination to a positive volume
// identity. SentinelValue is intentionally not secret; it should be a stable,
// unique token stored in SentinelFile at the root of the intended volume.
// This makes a missing mount fail closed instead of writing into an ordinary
// directory on the system disk.
type CopyDestinationVolume struct {
	Mode                CopyVolumeMode `json:"mode"`
	MountPoint          string         `json:"mount_point"`
	SentinelFile        string         `json:"sentinel_file"`
	SentinelValue       string         `json:"sentinel_value"`
	FilesystemUUID      string         `json:"filesystem_uuid,omitempty"`
	ExpectedFilesystem  string         `json:"expected_filesystem,omitempty"`
	ExpectedVolumeLabel string         `json:"expected_volume_label,omitempty"`
	MountOptions        []string       `json:"mount_options,omitempty"`
	WarmupSeconds       int            `json:"warmup_seconds,omitempty"`
	CooldownSeconds     int            `json:"cooldown_seconds,omitempty"`
	Spindown            bool           `json:"spindown,omitempty"`
}

func normalizeCopyDestinationVolume(volume CopyDestinationVolume, destination CopyEndpoint) (CopyDestinationVolume, error) {
	volume.Mode = CopyVolumeMode(strings.ToLower(strings.TrimSpace(string(volume.Mode))))
	volume.MountPoint = strings.TrimSpace(volume.MountPoint)
	volume.SentinelFile = strings.TrimSpace(volume.SentinelFile)
	if volume.SentinelFile == "" {
		volume.SentinelFile = defaultCopyVolumeSentinelFile
	}
	volume.SentinelValue = strings.TrimSpace(volume.SentinelValue)
	volume.FilesystemUUID = strings.ToLower(strings.TrimSpace(volume.FilesystemUUID))
	volume.ExpectedFilesystem = strings.ToLower(strings.TrimSpace(volume.ExpectedFilesystem))
	volume.ExpectedVolumeLabel = strings.TrimSpace(volume.ExpectedVolumeLabel)
	if volume.MountPoint != "" {
		absolute, err := filepath.Abs(filepath.Clean(volume.MountPoint))
		if err != nil || !filepath.IsAbs(absolute) {
			return CopyDestinationVolume{}, fmt.Errorf("copy destination volume mount point must be absolute")
		}
		volume.MountPoint = absolute
	}
	options := make([]string, 0, len(volume.MountOptions)+4)
	seen := make(map[string]struct{}, len(volume.MountOptions)+4)
	for _, option := range volume.MountOptions {
		option = strings.TrimSpace(option)
		key := strings.ToLower(option)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, option)
	}
	if volume.Mode == CopyVolumeManagedLinuxBlockDevice {
		for _, safeDefault := range []string{"rw", "nodev", "nosuid", "noexec"} {
			if _, exists := seen[safeDefault]; !exists {
				seen[safeDefault] = struct{}{}
				options = append(options, safeDefault)
			}
		}
	}
	volume.MountOptions = options
	if err := validateNormalizedCopyDestinationVolume(volume, destination); err != nil {
		return CopyDestinationVolume{}, err
	}
	return volume, nil
}

func validateNormalizedCopyDestinationVolume(volume CopyDestinationVolume, destination CopyEndpoint) error {
	if destination.Kind != CopyEndpointLocal {
		return fmt.Errorf("copy destination volume policy is only valid for a local destination")
	}
	switch volume.Mode {
	case CopyVolumeAlreadyMounted, CopyVolumeOSManaged, CopyVolumeManagedLinuxBlockDevice:
	default:
		return fmt.Errorf("unsupported copy destination volume mode %q", volume.Mode)
	}
	if volume.MountPoint == "" || !filepath.IsAbs(volume.MountPoint) || filepath.Clean(volume.MountPoint) != volume.MountPoint {
		return fmt.Errorf("copy destination volume mount point must be an absolute normalized path")
	}
	if filepath.Dir(volume.MountPoint) == volume.MountPoint {
		return fmt.Errorf("copy destination volume mount point cannot be a filesystem root")
	}
	if !copyPathAtOrWithin(volume.MountPoint, destination.Location) {
		return fmt.Errorf("copy destination %s must be at or below volume mount point %s", destination.Location, volume.MountPoint)
	}
	if volume.SentinelFile == "" || filepath.Base(volume.SentinelFile) != volume.SentinelFile ||
		volume.SentinelFile == "." || volume.SentinelFile == ".." || strings.ContainsAny(volume.SentinelFile, `/\\`) || hasUnsafeCopyText(volume.SentinelFile) {
		return fmt.Errorf("copy destination volume sentinel must be one file name at the mount root")
	}
	if len(volume.SentinelValue) < 8 || len(volume.SentinelValue) > 256 || hasUnsafeCopyText(volume.SentinelValue) || containsWhitespace(volume.SentinelValue) {
		return fmt.Errorf("copy destination volume sentinel value must be a unique 8-256 character token without whitespace")
	}
	if volume.WarmupSeconds < 0 || volume.WarmupSeconds > 3600 || volume.CooldownSeconds < 0 || volume.CooldownSeconds > 3600 {
		return fmt.Errorf("copy destination volume warmup and cooldown must be between 0 and 3600 seconds")
	}
	if volume.Mode != CopyVolumeManagedLinuxBlockDevice {
		if volume.FilesystemUUID != "" || volume.ExpectedFilesystem != "" || volume.ExpectedVolumeLabel != "" || len(volume.MountOptions) != 0 || volume.WarmupSeconds != 0 || volume.CooldownSeconds != 0 || volume.Spindown {
			return fmt.Errorf("verify-only copy destination volume modes cannot contain Linux mount lifecycle settings")
		}
		return nil
	}
	if !validCopyVolumeToken(volume.FilesystemUUID, 1, 128) {
		return fmt.Errorf("managed Linux destination volume requires a valid filesystem UUID")
	}
	if !validCopyVolumeToken(volume.ExpectedFilesystem, 1, 32) {
		return fmt.Errorf("managed Linux destination volume requires an expected filesystem")
	}
	if len(volume.ExpectedVolumeLabel) > 255 || hasUnsafeCopyText(volume.ExpectedVolumeLabel) {
		return fmt.Errorf("managed Linux destination volume label is invalid")
	}
	if len(volume.MountOptions) == 0 || len(volume.MountOptions) > 32 {
		return fmt.Errorf("managed Linux destination volume requires 1-32 explicit mount options")
	}
	seen := make(map[string]struct{}, len(volume.MountOptions))
	for _, option := range volume.MountOptions {
		if option == "" || len(option) > 256 || hasUnsafeCopyText(option) || strings.ContainsAny(option, ", \t") {
			return fmt.Errorf("managed Linux destination volume mount option %q is invalid", option)
		}
		key := strings.ToLower(option)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("managed Linux destination volume mount option %q is duplicated", option)
		}
		seen[key] = struct{}{}
		if key == "dev" || key == "suid" || key == "exec" {
			return fmt.Errorf("managed Linux destination volume mount option %q weakens the backup mount", option)
		}
		optionName, _, _ := strings.Cut(key, "=")
		if strings.Contains(optionName, "password") || strings.Contains(optionName, "passwd") || strings.Contains(optionName, "secret") || strings.Contains(optionName, "token") || strings.Contains(optionName, "key") || strings.Contains(optionName, "credential") {
			return fmt.Errorf("managed Linux destination volume mount option %q could expose a secret; block-device mounts must not carry credentials", optionName)
		}
	}
	for _, required := range []string{"rw", "nodev", "nosuid", "noexec"} {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("managed Linux destination volume mount options must include %s", required)
		}
	}
	return nil
}

func copyPathAtOrWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == candidate {
		return true
	}
	return pathWithin(root, candidate)
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}

func validCopyVolumeToken(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum || hasUnsafeCopyText(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:+-", character) {
			continue
		}
		return false
	}
	return true
}

func (volume CopyDestinationVolume) leaseKey() string {
	// A lease protects the physical/canonical destination, not a configurable
	// sentinel token. Otherwise two jobs aimed at one disk could choose
	// different expected tokens and incorrectly acquire independent leases.
	identity := "mount:" + filepath.Clean(volume.MountPoint)
	if volume.Mode == CopyVolumeManagedLinuxBlockDevice && volume.FilesystemUUID != "" {
		identity = "filesystem-uuid:" + strings.ToLower(volume.FilesystemUUID)
	}
	digest := sha256.Sum256([]byte("dbterm-copy-volume-v2\x00" + identity))
	return "copy-volume:" + hex.EncodeToString(digest[:])
}
