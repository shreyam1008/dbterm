package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	copyVolumeSentinelMaxBytes = 4096
	copyVolumeReleaseTimeout   = 15 * time.Minute
)

type copyVolumeCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// copyVolumeOperations makes every platform, command, filesystem, and wait
// boundary injectable. Unit tests never invoke mount, umount, sync, blkid,
// findmnt, or a power-management program.
type copyVolumeOperations struct {
	platform           string
	now                func() time.Time
	lstat              func(string) (os.FileInfo, error)
	open               func(string) (*os.File, error)
	evalSymlinks       func(string) (string, error)
	filesystemIdentity func(string, os.FileInfo) (string, error)
	isReparsePoint     func(string, os.FileInfo) bool
	run                func(context.Context, string, ...string) (copyVolumeCommandResult, error)
	wait               func(context.Context, time.Duration) error
}

func defaultCopyVolumeOperations() copyVolumeOperations {
	return copyVolumeOperations{
		platform:           runtime.GOOS,
		now:                time.Now,
		lstat:              os.Lstat,
		open:               os.Open,
		evalSymlinks:       filepath.EvalSymlinks,
		filesystemIdentity: copyVolumeFilesystemIdentity,
		isReparsePoint:     fileSetPathIsReparsePoint,
		run:                runCopyVolumeCommand,
		wait:               waitForCopyVolume,
	}
}

func runCopyVolumeCommand(ctx context.Context, name string, arguments ...string) (copyVolumeCommandResult, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := copyVolumeCommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	if err == nil {
		return result, nil
	}
	result.ExitCode = -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
	}
	return result, err
}

func waitForCopyVolume(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type copyVolumeSession struct {
	store               *Store
	configuration       CopyDestinationVolume
	destination         string
	operations          copyVolumeOperations
	lease               CopyVolumeLease
	device              string
	mountedByUs         bool
	leaseLost           bool
	released            bool
	publicationIdentity *copyVolumePublicationIdentity
}

type copyVolumePublicationIdentity struct {
	mountPointInfo       os.FileInfo
	destinationInfo      os.FileInfo
	sentinelInfo         os.FileInfo
	mountPointReal       string
	destinationReal      string
	sentinelPath         string
	filesystemOrVolumeID string
}

// VerifyCopyDestinationVolumeIdentity performs the read-only, positive volume
// identity check used by endpoint tests. It deliberately does not claim a
// lease, mount, unmount, sync, or power-manage a device; an actual copy run
// repeats this check while holding the durable destination-volume lease.
func VerifyCopyDestinationVolumeIdentity(job CopyJob) error {
	if job.DestinationVolume == nil {
		return nil
	}
	configuration, err := normalizeCopyDestinationVolume(*job.DestinationVolume, job.Destination)
	if err != nil {
		return err
	}
	session := &copyVolumeSession{
		configuration: configuration,
		destination:   job.Destination.Location,
		operations:    defaultCopyVolumeOperations(),
	}
	return session.verifyIdentity()
}

// prepareCopyDestinationVolume claims the volume before inspecting or
// mounting it. A nil session means this legacy/local job has no configured
// volume lifecycle policy.
func prepareCopyDestinationVolume(ctx context.Context, store *Store, job CopyJob, runID string, operations copyVolumeOperations) (*copyVolumeSession, error) {
	if job.DestinationVolume == nil {
		return nil, nil
	}
	if store == nil {
		return nil, fmt.Errorf("backup store is required for a managed copy destination volume")
	}
	if err := validateNormalizedCopyDestinationVolume(*job.DestinationVolume, job.Destination); err != nil {
		return nil, err
	}
	operations = completeCopyVolumeOperations(operations)
	now := operations.now().UTC()
	leaseFor := time.Duration(job.TimeoutMinutes+30) * time.Minute
	lease, err := store.ClaimCopyVolumeLease(ctx, job.DestinationVolume.leaseKey(), runID, job.ID, runID, now, now.Add(leaseFor))
	if err != nil {
		return nil, err
	}
	session := &copyVolumeSession{
		store: store, configuration: *job.DestinationVolume, destination: job.Destination.Location,
		operations: operations, lease: lease,
	}
	prepareErr := session.prepare(ctx)
	if prepareErr == nil {
		session.publicationIdentity, prepareErr = session.readVerifiedIdentity()
	}
	if prepareErr != nil {
		releaseContext, cancel := context.WithTimeout(context.Background(), copyVolumeReleaseTimeout)
		releaseWarnings := session.release(releaseContext, false)
		cancel()
		if len(releaseWarnings) > 0 {
			return nil, errors.Join(prepareErr, fmt.Errorf("clean up failed destination-volume preparation: %s", strings.Join(releaseWarnings, "; ")))
		}
		return nil, prepareErr
	}
	return session, nil
}

func completeCopyVolumeOperations(operations copyVolumeOperations) copyVolumeOperations {
	defaults := defaultCopyVolumeOperations()
	if strings.TrimSpace(operations.platform) == "" {
		operations.platform = defaults.platform
	}
	if operations.now == nil {
		operations.now = defaults.now
	}
	if operations.lstat == nil {
		operations.lstat = defaults.lstat
	}
	if operations.open == nil {
		operations.open = defaults.open
	}
	if operations.evalSymlinks == nil {
		operations.evalSymlinks = defaults.evalSymlinks
	}
	if operations.filesystemIdentity == nil {
		operations.filesystemIdentity = defaults.filesystemIdentity
	}
	if operations.isReparsePoint == nil {
		operations.isReparsePoint = defaults.isReparsePoint
	}
	if operations.run == nil {
		operations.run = defaults.run
	}
	if operations.wait == nil {
		operations.wait = defaults.wait
	}
	return operations
}

func (session *copyVolumeSession) prepare(ctx context.Context) error {
	configuration := session.configuration
	if configuration.Mode != CopyVolumeManagedLinuxBlockDevice {
		return session.verifyIdentity()
	}
	if session.operations.platform != "linux" {
		return fmt.Errorf("managed Linux block-device destinations are unavailable on %s", session.operations.platform)
	}
	if err := session.verifyMountPointDirectory(); err != nil {
		return err
	}
	device, err := session.linuxFilesystemDevice(ctx)
	if err != nil {
		return err
	}
	session.device = device
	mount, err := session.linuxMountState(ctx)
	if err != nil {
		return err
	}
	if mount.Mounted {
		if filepath.Clean(mount.Target) != configuration.MountPoint {
			return fmt.Errorf("configured filesystem UUID is already mounted at %s, not %s", mount.Target, configuration.MountPoint)
		}
		if !strings.EqualFold(mount.Filesystem, configuration.ExpectedFilesystem) {
			return fmt.Errorf("mounted filesystem is %q; expected %q", mount.Filesystem, configuration.ExpectedFilesystem)
		}
	} else {
		arguments := []string{"-t", configuration.ExpectedFilesystem}
		if len(configuration.MountOptions) > 0 {
			arguments = append(arguments, "-o", strings.Join(configuration.MountOptions, ","))
		}
		arguments = append(arguments, device, configuration.MountPoint)
		result, commandErr := session.operations.run(ctx, "mount", arguments...)
		if commandErr != nil {
			// An error leaves mount ownership ambiguous: an OS automounter may
			// have won the race. Never claim ownership or unmount in that state.
			return copyVolumeCommandFailure("mount configured destination volume", result, commandErr)
		}
		session.mountedByUs = true
		mount, err = session.linuxMountState(ctx)
		if err != nil {
			return fmt.Errorf("verify destination volume after mount: %w", err)
		}
		if !mount.Mounted || filepath.Clean(mount.Target) != configuration.MountPoint || !strings.EqualFold(mount.Filesystem, configuration.ExpectedFilesystem) {
			return fmt.Errorf("mount command returned success but the configured filesystem is not mounted at %s", configuration.MountPoint)
		}
	}
	if configuration.WarmupSeconds > 0 {
		if err := session.operations.wait(ctx, time.Duration(configuration.WarmupSeconds)*time.Second); err != nil {
			return fmt.Errorf("wait for destination volume warmup: %w", err)
		}
	}
	return session.verifyIdentity()
}

// verifyAfterTransfer is called before success is recorded. It catches a
// volume that disappeared or changed identity while bytes were being copied.
func (session *copyVolumeSession) verifyAfterTransfer(ctx context.Context) error {
	if session == nil {
		return nil
	}
	if err := session.verifyCapturedPublicationIdentity(ctx); err != nil {
		return fmt.Errorf("recheck destination volume after copy: %w", err)
	}
	return nil
}

// guardLocalPublication is wired into every local-destination CopyRunner. It
// binds each private stage and final artifact back to the exact mount point,
// sentinel, destination directory, and filesystem captured after preparation.
// The check is repeated around path inspection to narrow (but not pretend to
// eliminate) the unavoidable pathname TOCTOU window before rename/link.
func (session *copyVolumeSession) guardLocalPublication(ctx context.Context, localPath string, _ CopyLocalPublicationPhase) error {
	if session == nil {
		return nil
	}
	if err := session.verifyCapturedPublicationIdentity(ctx); err != nil {
		return err
	}
	localPath = filepath.Clean(localPath)
	destination := filepath.Clean(session.destination)
	if filepath.Dir(localPath) != destination {
		return fmt.Errorf("local publication path escaped the captured copy destination")
	}
	if err := session.rejectWindowsReparsePath(localPath); err != nil {
		return err
	}
	initial, err := session.operations.lstat(localPath)
	if err != nil {
		return fmt.Errorf("inspect local publication file: %w", err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || session.operations.isReparsePoint(localPath, initial) || !initial.Mode().IsRegular() {
		return fmt.Errorf("local publication file must be a real regular file, not a symbolic link or reparse point: %s", localPath)
	}
	filesystem, err := session.volumeFilesystemIdentity(localPath, initial, "local publication file")
	if err != nil {
		return err
	}
	if filesystem != session.publicationIdentity.filesystemOrVolumeID {
		return fmt.Errorf("local publication file is not on the captured destination filesystem or volume")
	}
	file, err := session.operations.open(localPath)
	if err != nil {
		return fmt.Errorf("open local publication file for identity check: %w", err)
	}
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !os.SameFile(initial, opened) {
		return fmt.Errorf("local publication file changed while it was opened")
	}
	if closeErr != nil {
		return fmt.Errorf("close local publication file after identity check: %w", closeErr)
	}
	current, err := session.operations.lstat(localPath)
	if err != nil || !os.SameFile(initial, current) {
		return fmt.Errorf("local publication file changed during identity verification")
	}
	if err := session.verifyCapturedPublicationIdentity(ctx); err != nil {
		return err
	}
	return nil
}

func (session *copyVolumeSession) verifyCapturedPublicationIdentity(ctx context.Context) error {
	if session == nil {
		return nil
	}
	if session.publicationIdentity == nil {
		return fmt.Errorf("destination volume publication identity was not captured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.configuration.Mode == CopyVolumeManagedLinuxBlockDevice {
		mount, err := session.linuxMountState(ctx)
		if err != nil {
			return fmt.Errorf("recheck managed destination mount: %w", err)
		}
		if !mount.Mounted || filepath.Clean(mount.Target) != session.configuration.MountPoint || !strings.EqualFold(mount.Filesystem, session.configuration.ExpectedFilesystem) {
			return fmt.Errorf("managed destination volume changed or disappeared during the copy")
		}
	}
	current, err := session.readVerifiedIdentity()
	if err != nil {
		return err
	}
	captured := session.publicationIdentity
	if !os.SameFile(captured.mountPointInfo, current.mountPointInfo) || captured.mountPointReal != current.mountPointReal {
		return fmt.Errorf("destination volume mount point changed after preparation")
	}
	if !os.SameFile(captured.destinationInfo, current.destinationInfo) || captured.destinationReal != current.destinationReal {
		return fmt.Errorf("copy directory on destination volume changed after preparation")
	}
	if !os.SameFile(captured.sentinelInfo, current.sentinelInfo) || captured.sentinelPath != current.sentinelPath ||
		captured.sentinelInfo.Size() != current.sentinelInfo.Size() || !captured.sentinelInfo.ModTime().Equal(current.sentinelInfo.ModTime()) {
		return fmt.Errorf("destination volume identity sentinel changed after preparation")
	}
	if captured.filesystemOrVolumeID != current.filesystemOrVolumeID {
		return fmt.Errorf("destination filesystem or volume changed after preparation")
	}
	return nil
}

func (session *copyVolumeSession) renewForPostProcessing(ctx context.Context, duration time.Duration) error {
	if session == nil {
		return nil
	}
	if duration < copyVolumeReleaseTimeout {
		duration = copyVolumeReleaseTimeout
	}
	now := session.operations.now().UTC()
	if err := session.store.RenewCopyVolumeLease(ctx, &session.lease, now, now.Add(duration)); err != nil {
		session.leaseLost = true
		return err
	}
	return nil
}

func (session *copyVolumeSession) verifyMountPointDirectory() error {
	_, err := session.inspectVolumeDirectory(session.configuration.MountPoint, "destination volume mount point")
	return err
}

func (session *copyVolumeSession) verifyIdentity() error {
	_, err := session.readVerifiedIdentity()
	return err
}

func (session *copyVolumeSession) readVerifiedIdentity() (*copyVolumePublicationIdentity, error) {
	mountInfo, err := session.inspectVolumeDirectory(session.configuration.MountPoint, "destination volume mount point")
	if err != nil {
		return nil, err
	}
	mountReal, err := session.operations.evalSymlinks(session.configuration.MountPoint)
	if err != nil {
		return nil, fmt.Errorf("resolve destination volume mount point: %w", err)
	}
	destinationInfo, err := session.inspectVolumeDirectory(session.destination, "copy directory on destination volume")
	if err != nil {
		return nil, err
	}
	destinationReal, err := session.operations.evalSymlinks(session.destination)
	if err != nil {
		return nil, fmt.Errorf("resolve copy directory on destination volume: %w", err)
	}
	if !copyPathAtOrWithin(filepath.Clean(mountReal), filepath.Clean(destinationReal)) {
		return nil, fmt.Errorf("resolved copy destination escaped the configured volume mount point")
	}

	sentinelPath := filepath.Join(session.configuration.MountPoint, session.configuration.SentinelFile)
	if err := session.rejectWindowsReparsePath(sentinelPath); err != nil {
		return nil, err
	}
	initial, err := session.operations.lstat(sentinelPath)
	if err != nil {
		return nil, fmt.Errorf("destination volume identity sentinel is missing: %w", err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || session.operations.isReparsePoint(sentinelPath, initial) || !initial.Mode().IsRegular() || initial.Size() < 1 || initial.Size() > copyVolumeSentinelMaxBytes {
		return nil, fmt.Errorf("destination volume identity sentinel must be a small regular file, not a symlink: %s", sentinelPath)
	}
	mountFilesystem, err := session.volumeFilesystemIdentity(session.configuration.MountPoint, mountInfo, "destination volume mount point")
	if err != nil {
		return nil, err
	}
	destinationFilesystem, err := session.volumeFilesystemIdentity(session.destination, destinationInfo, "copy directory on destination volume")
	if err != nil {
		return nil, err
	}
	sentinelFilesystem, err := session.volumeFilesystemIdentity(sentinelPath, initial, "destination volume identity sentinel")
	if err != nil {
		return nil, err
	}
	if destinationFilesystem != mountFilesystem || sentinelFilesystem != mountFilesystem {
		return nil, fmt.Errorf("destination volume mount point, identity sentinel, and copy directory must be on the same filesystem or volume")
	}
	file, err := session.operations.open(sentinelPath)
	if err != nil {
		return nil, fmt.Errorf("open destination volume identity sentinel: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(initial, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("destination volume identity sentinel changed while it was opened")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, copyVolumeSentinelMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read destination volume identity sentinel: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close destination volume identity sentinel: %w", closeErr)
	}
	if len(contents) > copyVolumeSentinelMaxBytes || strings.TrimSpace(string(contents)) != session.configuration.SentinelValue {
		return nil, fmt.Errorf("destination volume identity does not match the configured sentinel")
	}
	current, err := session.operations.lstat(sentinelPath)
	if err != nil || !os.SameFile(initial, current) || current.Size() != initial.Size() || current.ModTime() != initial.ModTime() {
		return nil, fmt.Errorf("destination volume identity sentinel changed during verification")
	}
	if current.Mode()&os.ModeSymlink != 0 || session.operations.isReparsePoint(sentinelPath, current) {
		return nil, fmt.Errorf("destination volume identity sentinel changed into a symbolic link or reparse point during verification")
	}
	if err := session.rejectWindowsReparsePath(sentinelPath); err != nil {
		return nil, err
	}
	if err := session.recheckVolumeDirectory(session.configuration.MountPoint, "destination volume mount point", mountInfo, mountFilesystem); err != nil {
		return nil, err
	}
	if err := session.recheckVolumeDirectory(session.destination, "copy directory on destination volume", destinationInfo, destinationFilesystem); err != nil {
		return nil, err
	}
	currentSentinelFilesystem, err := session.volumeFilesystemIdentity(sentinelPath, current, "destination volume identity sentinel")
	if err != nil {
		return nil, err
	}
	if currentSentinelFilesystem != mountFilesystem {
		return nil, fmt.Errorf("destination volume identity sentinel changed filesystem or volume during verification")
	}
	return &copyVolumePublicationIdentity{
		mountPointInfo: mountInfo, destinationInfo: destinationInfo, sentinelInfo: current,
		mountPointReal: filepath.Clean(mountReal), destinationReal: filepath.Clean(destinationReal),
		sentinelPath: filepath.Clean(sentinelPath), filesystemOrVolumeID: mountFilesystem,
	}, nil
}

func (session *copyVolumeSession) inspectVolumeDirectory(path, description string) (os.FileInfo, error) {
	if err := session.rejectWindowsReparsePath(path); err != nil {
		return nil, err
	}
	info, err := session.operations.lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", description, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || session.operations.isReparsePoint(path, info) || !info.IsDir() {
		return nil, fmt.Errorf("%s must be a real directory, not a symbolic link or reparse point: %s", description, path)
	}
	return info, nil
}

func (session *copyVolumeSession) recheckVolumeDirectory(path, description string, initial os.FileInfo, filesystem string) error {
	current, err := session.inspectVolumeDirectory(path, description)
	if err != nil || !os.SameFile(initial, current) {
		return fmt.Errorf("%s changed during volume identity verification", description)
	}
	currentFilesystem, err := session.volumeFilesystemIdentity(path, current, description)
	if err != nil {
		return err
	}
	if currentFilesystem != filesystem {
		return fmt.Errorf("%s changed filesystem or volume during identity verification", description)
	}
	return nil
}

func (session *copyVolumeSession) volumeFilesystemIdentity(path string, info os.FileInfo, description string) (string, error) {
	identity, err := session.operations.filesystemIdentity(path, info)
	if err != nil {
		return "", fmt.Errorf("identify filesystem or volume for %s: %w", description, err)
	}
	if strings.TrimSpace(identity) == "" {
		return "", fmt.Errorf("identify filesystem or volume for %s: empty identity", description)
	}
	return identity, nil
}

// Windows directory junctions and other name-surrogate reparse points may be
// present in any ancestor even when the final path itself looks like a normal
// directory. Reject every component before trusting containment or identity.
func (session *copyVolumeSession) rejectWindowsReparsePath(path string) error {
	if session.operations.platform != "windows" {
		return nil
	}
	prefixes, err := copyVolumePathPrefixes(path)
	if err != nil {
		return fmt.Errorf("inspect destination volume path components: %w", err)
	}
	for _, prefix := range prefixes {
		info, err := session.operations.lstat(prefix)
		if err != nil {
			return fmt.Errorf("inspect destination volume path component %s: %w", prefix, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || session.operations.isReparsePoint(prefix, info) {
			return fmt.Errorf("destination volume path contains a symbolic link or reparse point: %s", prefix)
		}
	}
	return nil
}

func copyVolumePathPrefixes(path string) ([]string, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("path must be absolute: %s", path)
	}
	root := filepath.VolumeName(cleaned) + string(filepath.Separator)
	if filepath.VolumeName(cleaned) == "" {
		root = string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, cleaned)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, fmt.Errorf("path cannot be enumerated from its volume root: %s", path)
	}
	prefixes := []string{filepath.Clean(root)}
	if relative == "." {
		return prefixes, nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("path contains a non-canonical component: %s", path)
		}
		current = filepath.Join(current, component)
		prefixes = append(prefixes, current)
	}
	return prefixes, nil
}

type linuxCopyMount struct {
	Mounted    bool
	Target     string
	Filesystem string
}

func (session *copyVolumeSession) linuxFilesystemDevice(ctx context.Context) (string, error) {
	configuration := session.configuration
	result, err := session.operations.run(ctx, "blkid", "-U", configuration.FilesystemUUID)
	if err != nil {
		return "", copyVolumeCommandFailure("resolve destination filesystem UUID", result, err)
	}
	device := strings.TrimSpace(result.Stdout)
	if device == "" || strings.ContainsAny(device, "\r\n\x00") || !filepath.IsAbs(device) {
		return "", fmt.Errorf("filesystem UUID resolved to an invalid block-device path")
	}
	info, err := session.operations.lstat(device)
	if err != nil {
		return "", fmt.Errorf("inspect destination block device: %w", err)
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("filesystem UUID did not resolve to a block device: %s", device)
	}
	checks := []struct {
		field string
		want  string
	}{
		{field: "UUID", want: configuration.FilesystemUUID},
		{field: "TYPE", want: configuration.ExpectedFilesystem},
	}
	if configuration.ExpectedVolumeLabel != "" {
		checks = append(checks, struct {
			field string
			want  string
		}{field: "LABEL", want: configuration.ExpectedVolumeLabel})
	}
	for _, check := range checks {
		result, err = session.operations.run(ctx, "blkid", "-o", "value", "-s", check.field, device)
		if err != nil {
			return "", copyVolumeCommandFailure("verify destination filesystem "+strings.ToLower(check.field), result, err)
		}
		got := strings.TrimSpace(result.Stdout)
		matches := got == check.want
		if check.field == "UUID" || check.field == "TYPE" {
			matches = strings.EqualFold(got, check.want)
		}
		if !matches {
			return "", fmt.Errorf("destination filesystem %s is %q; expected %q", strings.ToLower(check.field), got, check.want)
		}
	}
	return filepath.Clean(device), nil
}

func (session *copyVolumeSession) linuxMountState(ctx context.Context) (linuxCopyMount, error) {
	result, err := session.operations.run(ctx, "findmnt", "--json", "--source", session.device, "--output", "TARGET,FSTYPE")
	if err != nil {
		if result.ExitCode == 1 && strings.TrimSpace(result.Stdout) == "" {
			return linuxCopyMount{}, nil
		}
		return linuxCopyMount{}, copyVolumeCommandFailure("inspect destination filesystem mount", result, err)
	}
	var response struct {
		Filesystems []struct {
			Target     string `json:"target"`
			Filesystem string `json:"fstype"`
		} `json:"filesystems"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return linuxCopyMount{}, fmt.Errorf("decode destination filesystem mount state: %w", err)
	}
	if len(response.Filesystems) == 0 {
		return linuxCopyMount{}, nil
	}
	if len(response.Filesystems) != 1 || strings.TrimSpace(response.Filesystems[0].Target) == "" {
		return linuxCopyMount{}, fmt.Errorf("destination filesystem is mounted at multiple or invalid targets")
	}
	return linuxCopyMount{
		Mounted: true, Target: filepath.Clean(response.Filesystems[0].Target),
		Filesystem: strings.TrimSpace(response.Filesystems[0].Filesystem),
	}, nil
}

// release returns warnings rather than invalidating a completed copy. It only
// issues sync/unmount/spindown after renewing the exact lease owned by this run
// and only when this run itself mounted the volume.
func (session *copyVolumeSession) release(ctx context.Context, includeCooldown bool) []string {
	if session == nil || session.released {
		return nil
	}
	session.released = true
	if session.leaseLost {
		return nil
	}
	now := session.operations.now().UTC()
	if err := session.store.RenewCopyVolumeLease(ctx, &session.lease, now, now.Add(copyVolumeReleaseTimeout)); err != nil {
		return []string{"destination volume lease was lost; dbterm left the volume mounted and did not attempt power management: " + err.Error()}
	}
	var warnings []string
	if session.mountedByUs {
		if includeCooldown && session.configuration.CooldownSeconds > 0 {
			if err := session.operations.wait(ctx, time.Duration(session.configuration.CooldownSeconds)*time.Second); err != nil {
				warnings = append(warnings, "destination volume cooldown was interrupted: "+err.Error())
			}
		}
		mount, mountErr := session.linuxMountState(ctx)
		if mountErr != nil {
			warnings = append(warnings, "could not confirm destination volume before release; it was left mounted: "+mountErr.Error())
		} else if !mount.Mounted || filepath.Clean(mount.Target) != session.configuration.MountPoint {
			warnings = append(warnings, "destination volume was no longer mounted where dbterm placed it; no unmount or spindown was attempted")
		} else {
			result, syncErr := session.operations.run(ctx, "sync", "-f", session.configuration.MountPoint)
			if syncErr != nil {
				warnings = append(warnings, "destination volume sync failed; it was left mounted: "+copyVolumeCommandFailure("sync destination volume", result, syncErr).Error())
			} else {
				result, unmountErr := session.operations.run(ctx, "umount", session.configuration.MountPoint)
				if unmountErr != nil {
					warnings = append(warnings, "destination volume unmount failed; the verified copy was preserved: "+copyVolumeCommandFailure("unmount destination volume", result, unmountErr).Error())
				} else {
					remaining, verifyErr := session.linuxMountState(ctx)
					if verifyErr != nil {
						warnings = append(warnings, "destination volume unmount returned success but could not be verified; no spindown was attempted: "+verifyErr.Error())
					} else if remaining.Mounted {
						warnings = append(warnings, "destination volume unmount returned success but the volume is still mounted; no spindown was attempted")
					} else if session.configuration.Spindown {
						result, spindownErr := session.operations.run(ctx, "udisksctl", "power-off", "--block-device", session.device, "--no-user-interaction")
						if spindownErr != nil {
							warnings = append(warnings, "destination volume was unmounted but spindown failed: "+copyVolumeCommandFailure("power off destination volume", result, spindownErr).Error())
						}
					}
				}
			}
		}
	}
	if err := session.store.ReleaseCopyVolumeLease(ctx, session.lease); err != nil {
		warnings = append(warnings, "destination volume lease could not be released: "+err.Error())
	}
	return warnings
}

func copyVolumeCommandFailure(action string, result copyVolumeCommandResult, commandErr error) error {
	detail := sanitizeCopyVolumeCommandOutput(result.Stderr)
	if detail == "" {
		detail = sanitizeCopyVolumeCommandOutput(result.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, commandErr)
	}
	return fmt.Errorf("%s: %w (%s)", action, commandErr, detail)
}

func sanitizeCopyVolumeCommandOutput(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 512 {
		value = value[:512] + "..."
	}
	return value
}
