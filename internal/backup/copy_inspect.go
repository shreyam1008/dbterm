package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

// CopyInspectionLocation selects which independently stored recovery point is
// materialized. A push normally inspects its destination; a pull can inspect
// either its local destination or the remote producer copy.
type CopyInspectionLocation string

const (
	CopyInspectionSource      CopyInspectionLocation = "source"
	CopyInspectionDestination CopyInspectionLocation = "destination"
)

// CopyInspectionStageOptions controls private materialization. StagingRoot is
// primarily useful to applications that already own a private state root. When
// empty, dbterm's native private backup staging root is used.
type CopyInspectionStageOptions struct {
	Location    CopyInspectionLocation
	StagingRoot string
}

// SelectCopyArtifactForInspection returns one exact completed, unpruned copy
// record. With no requested ID it deterministically selects the newest source
// recovery point and refuses ambiguous catalog identities.
func SelectCopyArtifactForInspection(runs []CopyRun, requestedID string) (CopyArtifactResult, error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		matches := make([]CopyArtifactResult, 0, 1)
		eligible := make([]CopyArtifactResult, 0, 1)
		for _, run := range runs {
			for _, artifact := range run.Artifacts {
				if artifact.ArtifactID != requestedID {
					continue
				}
				matches = append(matches, artifact)
				if artifact.PublicationState == ArtifactPublicationComplete && artifact.PrunedAt.IsZero() {
					eligible = append(eligible, artifact)
				}
			}
		}
		if len(matches) == 0 {
			return CopyArtifactResult{}, fmt.Errorf("artifact ID %q was not found in recorded copy runs", requestedID)
		}
		if len(eligible) == 1 {
			return eligible[0], nil
		}
		if len(eligible) > 1 {
			return CopyArtifactResult{}, fmt.Errorf("artifact ID %q is ambiguous across %d completed catalog records", requestedID, len(eligible))
		}
		for _, match := range matches {
			if match.PublicationState != ArtifactPublicationComplete {
				return CopyArtifactResult{}, fmt.Errorf("artifact ID %q is incomplete (%s)", requestedID, match.PublicationState)
			}
		}
		return CopyArtifactResult{}, fmt.Errorf("artifact ID %q was pruned at %s", requestedID, matches[0].PrunedAt.UTC().Format(time.RFC3339))
	}

	candidates := make([]CopyArtifactResult, 0)
	seen := make(map[string]struct{})
	for _, run := range runs {
		for _, artifact := range run.Artifacts {
			if artifact.PublicationState != ArtifactPublicationComplete || !artifact.PrunedAt.IsZero() {
				continue
			}
			if strings.TrimSpace(artifact.ArtifactID) == "" {
				return CopyArtifactResult{}, fmt.Errorf("completed copy catalog record has no artifact ID")
			}
			if _, duplicate := seen[artifact.ArtifactID]; duplicate {
				return CopyArtifactResult{}, fmt.Errorf("artifact ID %q is ambiguous across multiple completed catalog records", artifact.ArtifactID)
			}
			seen[artifact.ArtifactID] = struct{}{}
			candidates = append(candidates, artifact)
		}
	}
	if len(candidates) == 0 {
		return CopyArtifactResult{}, fmt.Errorf("no unpruned completed copied artifacts are recorded")
	}
	sortCopyArtifactsForInspection(candidates)
	return candidates[0], nil
}

func copyArtifactSelectionTime(artifact CopyArtifactResult) time.Time {
	if !artifact.SourceCreatedAt.IsZero() {
		return artifact.SourceCreatedAt
	}
	return artifact.VerifiedAt
}

// StagedCopyArtifact is an independently verified, local view of one durable
// copy result. Call Cleanup when inspection or restore preview is complete.
// Path has its completion sidecar beside it, so it can be passed directly to
// Inspect and the existing guarded restore flow.
type StagedCopyArtifact struct {
	Path         string
	ManifestPath string
	Origin       string
	Manifest     ArtifactManifest

	directory     string
	directoryInfo os.FileInfo
	cleanupOnce   sync.Once
	cleanupErr    error
}

// Inspect enters the ordinary byte-based inspection flow after remote or
// copied storage has been staged and verified.
func (staged *StagedCopyArtifact) Inspect(ctx context.Context, options InspectOptions) (*Inspection, error) {
	if staged == nil || strings.TrimSpace(staged.Path) == "" {
		return nil, fmt.Errorf("staged copy artifact is required")
	}
	return Inspect(ctx, staged.Path, options)
}

// Cleanup removes only the two files and exact private directory created by
// StageCopyArtifactForInspection. Unexpected entries or directory replacement
// are preserved for review rather than recursively removed.
func (staged *StagedCopyArtifact) Cleanup() error {
	if staged == nil {
		return nil
	}
	staged.cleanupOnce.Do(func() {
		staged.cleanupErr = staged.cleanup()
	})
	return staged.cleanupErr
}

// Close is an io.Closer-style alias for Cleanup, allowing callers to simply
// defer staged.Close() around an inspect or restore preview.
func (staged *StagedCopyArtifact) Close() error {
	return staged.Cleanup()
}

func (staged *StagedCopyArtifact) cleanup() error {
	if staged.directory == "" {
		return nil
	}
	current, err := os.Lstat(staged.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect private copy-inspection stage: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || staged.directoryInfo == nil || !os.SameFile(staged.directoryInfo, current) {
		return fmt.Errorf("private copy-inspection stage changed; preserved for review: %s", staged.directory)
	}
	entries, err := os.ReadDir(staged.directory)
	if err != nil {
		return fmt.Errorf("list private copy-inspection stage: %w", err)
	}
	allowed := map[string]struct{}{
		filepath.Base(staged.Path):         {},
		filepath.Base(staged.ManifestPath): {},
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || entry.IsDir() {
			return fmt.Errorf("private copy-inspection stage contains an unexpected entry; preserved for review: %s", filepath.Join(staged.directory, entry.Name()))
		}
	}
	for _, file := range []string{staged.ManifestPath, staged.Path} {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove private copy-inspection file %s: %w", file, err)
		}
	}
	if err := os.Remove(staged.directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private copy-inspection stage %s: %w", staged.directory, err)
	}
	return nil
}

type copyInspectionManifestSnapshot struct {
	manifest ArtifactManifest
	data     []byte
	size     int64
	digest   string
}

// StageCopyArtifactForInspection materializes one completed CopyArtifactResult
// into private local storage. It re-reads the sidecar as the publication
// signal, binds it to the catalog identity, streams the exact artifact bytes,
// verifies SHA-256 and the lightweight format envelope, and checks the source
// did not change during transfer.
func StageCopyArtifactForInspection(ctx context.Context, job CopyJob, result CopyArtifactResult, options CopyInspectionStageOptions) (_ *StagedCopyArtifact, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Location == "" {
		options.Location = CopyInspectionDestination
	}
	if options.Location != CopyInspectionSource && options.Location != CopyInspectionDestination {
		return nil, fmt.Errorf("unsupported copy inspection location %q", options.Location)
	}
	if err := result.validate(CopyVerificationSizeOnly, true); err != nil {
		return nil, fmt.Errorf("copy artifact catalog result is not inspectable: %w", err)
	}
	if !result.PrunedAt.IsZero() {
		return nil, fmt.Errorf("copied artifact %q was pruned at %s and cannot be inspected", result.ArtifactID, result.PrunedAt.UTC().Format(time.RFC3339))
	}
	if !validCopySHA256(result.SHA256) {
		return nil, fmt.Errorf("copy artifact catalog result requires a complete SHA-256 for inspection")
	}
	if err := validateCopyArtifactFilter(job.ArtifactFilter); err != nil {
		return nil, fmt.Errorf("copy job artifact filter: %w", err)
	}

	endpoint, display, err := copyInspectionEndpoint(job, result, options.Location)
	if err != nil {
		return nil, err
	}
	artifactName, artifactLocation, manifestLocation, err := resolveCopyInspectionLocation(job, endpoint, display, result, options.Location)
	if err != nil {
		return nil, err
	}

	staged, output, err := newCopyInspectionStage(options.StagingRoot, artifactName)
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		if !completed {
			_ = output.Close()
			if cleanupErr := staged.Cleanup(); cleanupErr != nil {
				if err == nil {
					err = cleanupErr
				} else {
					err = fmt.Errorf("%w; private staging cleanup also failed: %v", err, cleanupErr)
				}
			}
		}
	}()

	var snapshot copyInspectionManifestSnapshot
	switch endpoint.Kind {
	case CopyEndpointLocal:
		snapshot, err = materializeLocalCopyForInspection(ctx, job, result, options.Location, artifactLocation, manifestLocation, output)
	case CopyEndpointSSH, CopyEndpointSFTP:
		snapshot, err = materializeSFTPCopyForInspection(ctx, job, result, options.Location, endpoint, artifactLocation, manifestLocation, output)
	case CopyEndpointRclone:
		if options.Location != CopyInspectionSource || job.Mode != CopyModePull {
			err = fmt.Errorf("rclone copy inspection is supported only for a pull job source")
		} else {
			snapshot, err = materializeRcloneCopyForInspection(ctx, job, result, options.Location, artifactLocation, manifestLocation, output)
		}
	default:
		err = fmt.Errorf("copy endpoint kind %q cannot be staged for inspection", endpoint.Kind)
	}
	if err != nil {
		return nil, err
	}
	if err := output.Sync(); err != nil {
		return nil, fmt.Errorf("sync private copy-inspection artifact: %w", err)
	}
	if err := output.Close(); err != nil {
		return nil, fmt.Errorf("close private copy-inspection artifact: %w", err)
	}
	if err := verifyCopiedArtifactEnvelope(staged.Path, snapshot.manifest); err != nil {
		return nil, fmt.Errorf("verify staged copy artifact format: %w", err)
	}
	if err := writeCopyInspectionManifest(staged.ManifestPath, snapshot.data); err != nil {
		return nil, err
	}
	if err := syncDirectory(staged.directory); err != nil {
		return nil, fmt.Errorf("sync private copy-inspection stage: %w", err)
	}
	staged.Origin = display
	staged.Manifest = snapshot.manifest
	completed = true
	return staged, nil
}

func copyInspectionEndpoint(job CopyJob, result CopyArtifactResult, location CopyInspectionLocation) (CopyEndpoint, string, error) {
	endpoint := job.Destination
	display := result.Destination
	if location == CopyInspectionSource {
		endpoint = job.Source
		display = result.Source
	}
	if strings.TrimSpace(display) == "" || hasUnsafeCopyText(display) {
		return CopyEndpoint{}, "", fmt.Errorf("copy inspection %s path is invalid", location)
	}
	if endpoint.Kind == CopyEndpointRclone && (location != CopyInspectionSource || job.Mode != CopyModePull) {
		return CopyEndpoint{}, "", fmt.Errorf("rclone copy inspection is supported only for a pull job source")
	}
	if (endpoint.Kind == CopyEndpointSSH || endpoint.Kind == CopyEndpointSFTP) &&
		!((location == CopyInspectionSource && job.Mode == CopyModePull) || (location == CopyInspectionDestination && job.Mode == CopyModePush)) {
		return CopyEndpoint{}, "", fmt.Errorf("SSH/SFTP copy inspection supports pull sources and push destinations only")
	}
	allowReference := location == CopyInspectionSource && endpoint.Kind == CopyEndpointLocal && endpoint.Location == "" && strings.TrimSpace(job.SourceBackupJobID) != ""
	normalized, err := normalizeCopyEndpoint(endpoint, allowReference)
	if err != nil {
		return CopyEndpoint{}, "", fmt.Errorf("copy inspection %s endpoint: %w", location, err)
	}
	if normalized != endpoint {
		return CopyEndpoint{}, "", fmt.Errorf("copy inspection %s endpoint is not normalized", location)
	}
	return endpoint, display, nil
}

func resolveCopyInspectionLocation(job CopyJob, endpoint CopyEndpoint, display string, result CopyArtifactResult, location CopyInspectionLocation) (name, artifact, manifest string, err error) {
	switch endpoint.Kind {
	case CopyEndpointLocal:
		if !filepath.IsAbs(display) || filepath.Clean(display) != display {
			return "", "", "", fmt.Errorf("copy inspection local artifact path must be normalized and absolute")
		}
		name = filepath.Base(display)
		if err := validateCopyInspectionArtifactName(name); err != nil {
			return "", "", "", err
		}
		root := endpoint.Location
		if root == "" && location == CopyInspectionSource && job.SourceBackupJobID != "" {
			root = filepath.Dir(display)
		}
		if root == "" || filepath.Clean(filepath.Join(root, name)) != display || !pathWithin(root, display) {
			return "", "", "", fmt.Errorf("copy inspection artifact is not a direct child of its configured local endpoint")
		}
		rootInfo, statErr := os.Lstat(root)
		if statErr != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("path is not a real directory")
			}
			return "", "", "", fmt.Errorf("inspect copy endpoint root %s: %w", root, statErr)
		}
		artifact, manifest = display, artifactManifestPath(display)
	case CopyEndpointSSH, CopyEndpointSFTP:
		parsedEndpoint, parseErr := parseSFTPCopyEndpoint(endpoint)
		if parseErr != nil {
			return "", "", "", parseErr
		}
		parsedDisplay, parseErr := url.Parse(display)
		if parseErr != nil {
			return "", "", "", fmt.Errorf("parse copy inspection SFTP artifact: %w", parseErr)
		}
		remotePath, unescapeErr := url.PathUnescape(parsedDisplay.EscapedPath())
		if unescapeErr != nil {
			return "", "", "", fmt.Errorf("decode copy inspection SFTP artifact path: %w", unescapeErr)
		}
		name = path.Base(remotePath)
		if err := validateCopyInspectionArtifactName(name); err != nil {
			return "", "", "", err
		}
		artifact, err = safeSFTPChild(parsedEndpoint.root, name)
		if err != nil {
			return "", "", "", err
		}
		if remotePath != artifact || display != sftpDisplayPath(endpoint, name) {
			return "", "", "", fmt.Errorf("copy inspection artifact is not a direct child of its configured SFTP endpoint")
		}
		manifest, err = safeSFTPChild(parsedEndpoint.root, name+ArtifactManifestSuffix)
		if err != nil {
			return "", "", "", err
		}
	case CopyEndpointRclone:
		root, parseErr := parseDestination(endpoint.Location)
		if parseErr != nil || root.kind != destinationRclone {
			return "", "", "", fmt.Errorf("parse copy inspection rclone root")
		}
		object, parseErr := parseDestination(display)
		if parseErr != nil || object.kind != destinationRclone {
			return "", "", "", fmt.Errorf("parse copy inspection rclone artifact")
		}
		parent, objectName, parseErr := object.parentAndName()
		if parseErr != nil || parent.String() != root.String() {
			return "", "", "", fmt.Errorf("copy inspection artifact is not a direct child of its configured rclone endpoint")
		}
		name = objectName
		if err := validateCopyInspectionArtifactName(name); err != nil {
			return "", "", "", err
		}
		artifact = object.String()
		manifestObject, joinErr := root.join(name + ArtifactManifestSuffix)
		if joinErr != nil {
			return "", "", "", joinErr
		}
		manifest = manifestObject
	default:
		return "", "", "", fmt.Errorf("copy endpoint kind %q cannot be staged for inspection", endpoint.Kind)
	}

	if location == CopyInspectionDestination {
		expectedManifest := manifest
		if endpoint.Kind == CopyEndpointSSH || endpoint.Kind == CopyEndpointSFTP {
			expectedManifest = sftpDisplayPath(endpoint, name+ArtifactManifestSuffix)
		}
		if result.ManifestPath != expectedManifest {
			return "", "", "", fmt.Errorf("copy completion manifest path does not match its catalog destination")
		}
	}
	return name, artifact, manifest, nil
}

func validateCopyInspectionArtifactName(name string) error {
	if err := validateExactArtifactFilename(name); err != nil {
		return fmt.Errorf("copy inspection artifact name is unsafe: %w", err)
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".partial") || strings.HasPrefix(lower, ".dbterm-") {
		return fmt.Errorf("copy inspection refuses private or partial artifact %q", name)
	}
	return nil
}

func newCopyInspectionStage(configuredRoot, artifactName string) (*StagedCopyArtifact, *os.File, error) {
	root := strings.TrimSpace(configuredRoot)
	var err error
	var directory string
	if root == "" {
		directory, err = newPrivateNativeStage(time.Now())
		if err != nil {
			return nil, nil, err
		}
		root = filepath.Dir(directory)
	} else {
		if !filepath.IsAbs(root) {
			return nil, nil, fmt.Errorf("copy-inspection staging root must be absolute")
		}
		root, err = filepath.Abs(filepath.Clean(root))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve copy-inspection staging root: %w", err)
		}
		if err := privatefile.EnsurePrivateDirectory(root); err != nil {
			return nil, nil, fmt.Errorf("prepare private copy-inspection staging root: %w", err)
		}
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		if err == nil {
			err = fmt.Errorf("path is not a real directory")
		}
		if directory != "" {
			_ = os.Remove(directory)
		}
		return nil, nil, fmt.Errorf("inspect private copy-inspection staging root %s: %w", root, err)
	}
	if directory == "" {
		directory, err = privatefile.CreateTempDirectory(root, privateStagePrefix+"inspect-")
		if err != nil {
			return nil, nil, fmt.Errorf("create private copy-inspection stage: %w", err)
		}
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		_ = os.Remove(directory)
		return nil, nil, fmt.Errorf("inspect private copy-inspection stage: %w", err)
	}
	artifactPath := filepath.Join(directory, artifactName)
	manifestPath := artifactManifestPath(artifactPath)
	output, err := privatefile.Create(artifactPath)
	if err != nil {
		_ = os.Remove(directory)
		return nil, nil, fmt.Errorf("create private copy-inspection artifact: %w", err)
	}
	return &StagedCopyArtifact{
		Path: artifactPath, ManifestPath: manifestPath,
		directory: directory, directoryInfo: directoryInfo,
	}, output, nil
}

func materializeLocalCopyForInspection(ctx context.Context, job CopyJob, result CopyArtifactResult, location CopyInspectionLocation, artifactPath, manifestPath string, output *os.File) (copyInspectionManifestSnapshot, error) {
	first, err := readLocalCopyInspectionManifest(ctx, manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("read local copy completion manifest: %w", err)
	}
	if err := verifyCopyInspectionManifest(job, result, location, first); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	initial, err := os.Lstat(artifactPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("inspect local copy artifact: %w", err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() || initial.Size() != first.manifest.SizeBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("local copy artifact must be a regular non-symlink file of exactly %d bytes", first.manifest.SizeBytes)
	}
	source, err := os.Open(artifactPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("open local copy artifact: %w", err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("local copy artifact changed while it was being opened: %s", artifactPath)
	}
	digest, err := streamCopyInspectionArtifact(ctx, source, output, first.manifest.SizeBytes)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("stage local copy artifact: %w", err)
	}
	if !strings.EqualFold(digest, first.manifest.SHA256) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("local copy artifact SHA-256 does not match its completion manifest")
	}
	if err := verifyOpenedFileUnchanged(source, initial, artifactPath); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	second, err := readLocalCopyInspectionManifest(ctx, manifestPath)
	if err != nil || first.size != second.size || !strings.EqualFold(first.digest, second.digest) || !sameCopyManifest(first.manifest, second.manifest) {
		if err == nil {
			err = fmt.Errorf("completion manifest bytes changed")
		}
		return copyInspectionManifestSnapshot{}, fmt.Errorf("local copy completion manifest changed during staging: %w", err)
	}
	return second, nil
}

func readLocalCopyInspectionManifest(ctx context.Context, manifestPath string) (copyInspectionManifestSnapshot, error) {
	initial, err := os.Lstat(manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() || initial.Size() < 1 || initial.Size() > maxArtifactManifestBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("copy completion manifest must be a regular non-symlink file between 1 and %d bytes", maxArtifactManifestBytes)
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("copy completion manifest changed while it was being opened: %s", manifestPath)
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxArtifactManifestBytes+1))
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	if int64(len(data)) != initial.Size() {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("copy completion manifest changed while it was being read: %s", manifestPath)
	}
	if err := verifyOpenedFileUnchanged(file, initial, manifestPath); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	return decodeCopyInspectionManifest(data)
}

func materializeSFTPCopyForInspection(ctx context.Context, job CopyJob, result CopyArtifactResult, location CopyInspectionLocation, endpoint CopyEndpoint, artifactPath, manifestPath string, output *os.File) (copyInspectionManifestSnapshot, error) {
	parsedEndpoint, err := parseSFTPCopyEndpoint(endpoint)
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	connection, err := dialSFTPCopyEndpoint(ctx, endpoint)
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	defer connection.Close()
	if err := requireSFTPDirectory(connection.client, parsedEndpoint.root); err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("inspect SFTP copy endpoint root: %w", err)
	}
	first, err := readSFTPCopyInspectionManifest(ctx, connection.client, manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("read SFTP copy completion manifest: %w", err)
	}
	if err := verifyCopyInspectionManifest(job, result, location, first); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	initial, err := connection.client.Lstat(artifactPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("inspect SFTP copy artifact: %w", err)
	}
	if err := requireSFTPRegular(initial, artifactPath, first.manifest.SizeBytes); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	source, err := connection.client.Open(artifactPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, preferSFTPContextError(ctx, err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !sameSFTPFileState(initial, opened) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP copy artifact changed while it was being opened: %s", artifactPath)
	}
	digest, err := streamCopyInspectionArtifact(ctx, source, output, first.manifest.SizeBytes)
	if err != nil {
		return copyInspectionManifestSnapshot{}, preferSFTPContextError(ctx, fmt.Errorf("stage SFTP copy artifact: %w", err))
	}
	if !strings.EqualFold(digest, first.manifest.SHA256) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP copy artifact SHA-256 does not match its completion manifest")
	}
	if err := verifySFTPHandleAndPathUnchanged(connection.client, source, artifactPath, initial); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	second, err := readSFTPCopyInspectionManifest(ctx, connection.client, manifestPath)
	if err != nil || first.size != second.size || !strings.EqualFold(first.digest, second.digest) || !sameCopyManifest(first.manifest, second.manifest) {
		if err == nil {
			err = fmt.Errorf("completion manifest bytes changed")
		}
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP copy completion manifest changed during staging: %w", err)
	}
	return second, nil
}

func readSFTPCopyInspectionManifest(ctx context.Context, client *sftp.Client, manifestPath string) (copyInspectionManifestSnapshot, error) {
	initial, err := client.Lstat(manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, preferSFTPContextError(ctx, err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() || initial.Size() < 1 || initial.Size() > maxArtifactManifestBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP completion manifest must be a regular non-symlink file between 1 and %d bytes", maxArtifactManifestBytes)
	}
	file, err := client.Open(manifestPath)
	if err != nil {
		return copyInspectionManifestSnapshot{}, preferSFTPContextError(ctx, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSFTPFileState(initial, opened) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP completion manifest changed while it was being opened: %s", manifestPath)
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxArtifactManifestBytes+1))
	if err != nil {
		return copyInspectionManifestSnapshot{}, preferSFTPContextError(ctx, err)
	}
	if int64(len(data)) != initial.Size() {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("SFTP completion manifest changed while it was being read: %s", manifestPath)
	}
	if err := verifySFTPHandleAndPathUnchanged(client, file, manifestPath, initial); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	return decodeCopyInspectionManifest(data)
}

func materializeRcloneCopyForInspection(ctx context.Context, job CopyJob, result CopyArtifactResult, location CopyInspectionLocation, artifactPath, manifestPath string, output *os.File) (copyInspectionManifestSnapshot, error) {
	artifact, err := parseDestination(artifactPath)
	if err != nil || artifact.kind != destinationRclone {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("parse rclone copy artifact")
	}
	manifestObject, err := parseDestination(manifestPath)
	if err != nil || manifestObject.kind != destinationRclone {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("parse rclone copy completion manifest")
	}
	artifactItem, exists, err := inspectRcloneObject(ctx, artifact)
	if err != nil || !exists {
		if err == nil {
			err = os.ErrNotExist
		}
		return copyInspectionManifestSnapshot{}, fmt.Errorf("inspect rclone copy artifact: %w", err)
	}
	artifactVersion, err := rcloneCopyObjectVersion(artifactItem, artifact.String())
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	manifestItem, exists, err := inspectRcloneObject(ctx, manifestObject)
	if err != nil || !exists {
		if err == nil {
			err = os.ErrNotExist
		}
		return copyInspectionManifestSnapshot{}, fmt.Errorf("inspect rclone copy completion manifest: %w", err)
	}
	manifestVersion, err := rcloneCopyObjectVersion(manifestItem, manifestObject.String())
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	if manifestVersion.size < 1 || manifestVersion.size > maxArtifactManifestBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone completion manifest size must be between 1 and %d bytes", maxArtifactManifestBytes)
	}
	first, err := readRcloneCopyInspectionManifest(ctx, manifestObject, manifestVersion)
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	if err := verifyCopyInspectionManifest(job, result, location, first); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	if artifactVersion.size != first.manifest.SizeBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone copy artifact size is %d; completion manifest records %d", artifactVersion.size, first.manifest.SizeBytes)
	}
	hash := sha256.New()
	writer := &copyInspectionExactWriter{writer: io.MultiWriter(output, hash), maximum: first.manifest.SizeBytes}
	if err := runRclone(ctx, writer, "cat", artifact.rclonePath()); err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("stage rclone copy artifact: %w", err)
	}
	if writer.written != first.manifest.SizeBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone copy artifact wrote %d bytes; expected %d", writer.written, first.manifest.SizeBytes)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), first.manifest.SHA256) {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone copy artifact SHA-256 does not match its completion manifest")
	}
	if err := verifyRcloneCopyObjectUnchanged(ctx, artifact, artifactVersion, "during inspection staging"); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	second, err := readRcloneCopyInspectionManifest(ctx, manifestObject, manifestVersion)
	if err != nil || first.size != second.size || !strings.EqualFold(first.digest, second.digest) || !sameCopyManifest(first.manifest, second.manifest) {
		if err == nil {
			err = fmt.Errorf("completion manifest bytes changed")
		}
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone copy completion manifest changed during staging: %w", err)
	}
	return second, nil
}

func readRcloneCopyInspectionManifest(ctx context.Context, object destinationSpec, version rcloneCopyVersion) (copyInspectionManifestSnapshot, error) {
	buffer := &copyInspectionLimitedBuffer{maximum: maxArtifactManifestBytes}
	if err := runRclone(ctx, buffer, "cat", object.rclonePath()); err != nil {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("read rclone copy completion manifest: %w", err)
	}
	if buffer.overflow || int64(len(buffer.data)) != version.size {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("rclone copy completion manifest changed while it was being read")
	}
	if err := verifyRcloneCopyObjectUnchanged(ctx, object, version, "while it was being read"); err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	return decodeCopyInspectionManifest(buffer.data)
}

func verifyCopyInspectionManifest(job CopyJob, result CopyArtifactResult, location CopyInspectionLocation, snapshot copyInspectionManifestSnapshot) error {
	manifest := snapshot.manifest
	if manifest.ArtifactID != result.ArtifactID || manifest.SizeBytes != result.SizeBytes || !strings.EqualFold(manifest.SHA256, result.SHA256) {
		return fmt.Errorf("copy completion manifest does not match the catalog artifact identity, size, and SHA-256")
	}
	if !result.SourceCreatedAt.IsZero() && !manifest.CreatedAt.Equal(result.SourceCreatedAt) {
		return fmt.Errorf("copy completion manifest creation time does not match the catalog artifact")
	}
	if job.SourceBackupJobID != "" && manifest.JobID != job.SourceBackupJobID {
		return fmt.Errorf("copy completion manifest job ID does not match source backup job %q", job.SourceBackupJobID)
	}
	if !copyManifestMatches(manifest, job.ArtifactFilter) {
		return fmt.Errorf("copy completion manifest does not match the copy job artifact filter")
	}
	if (result.ManifestSize > 0 || strings.TrimSpace(result.ManifestSHA256) != "") &&
		(snapshot.size != result.ManifestSize || !strings.EqualFold(snapshot.digest, result.ManifestSHA256)) {
		return fmt.Errorf("copy completion manifest bytes do not match the durable catalog sidecar identity recorded at copy time")
	}
	return nil
}

func decodeCopyInspectionManifest(data []byte) (copyInspectionManifestSnapshot, error) {
	if len(data) < 1 || len(data) > maxArtifactManifestBytes {
		return copyInspectionManifestSnapshot{}, fmt.Errorf("copy completion manifest size must be between 1 and %d bytes", maxArtifactManifestBytes)
	}
	manifest, err := DecodeArtifactManifest(bytes.NewReader(data))
	if err != nil {
		return copyInspectionManifestSnapshot{}, err
	}
	digest := sha256.Sum256(data)
	return copyInspectionManifestSnapshot{
		manifest: *manifest, data: append([]byte(nil), data...), size: int64(len(data)), digest: hex.EncodeToString(digest[:]),
	}, nil
}

func streamCopyInspectionArtifact(ctx context.Context, source io.Reader, destination io.Writer, expected int64) (string, error) {
	if expected < 1 {
		return "", fmt.Errorf("copy inspection artifact size must be positive")
	}
	hash := sha256.New()
	writer := &copyInspectionExactWriter{writer: io.MultiWriter(destination, hash), maximum: expected}
	_, err := io.CopyBuffer(writer, &contextReader{ctx: ctx, reader: source}, make([]byte, 256*1024))
	if err != nil {
		return "", err
	}
	if writer.written != expected {
		return "", fmt.Errorf("copy inspection artifact wrote %d bytes; expected %d", writer.written, expected)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type copyInspectionExactWriter struct {
	writer  io.Writer
	maximum int64
	written int64
}

func (writer *copyInspectionExactWriter) Write(data []byte) (int, error) {
	remaining := writer.maximum - writer.written
	if remaining <= 0 && len(data) > 0 {
		return 0, fmt.Errorf("copy inspection artifact exceeds the size recorded by its completion manifest")
	}
	allowed := len(data)
	if int64(allowed) > remaining {
		allowed = int(remaining)
	}
	written, err := writer.writer.Write(data[:allowed])
	writer.written += int64(written)
	if err != nil {
		return written, err
	}
	if written != allowed {
		return written, io.ErrShortWrite
	}
	if allowed != len(data) {
		return written, fmt.Errorf("copy inspection artifact exceeds the size recorded by its completion manifest")
	}
	return written, nil
}

type copyInspectionLimitedBuffer struct {
	data     []byte
	maximum  int
	overflow bool
}

func (buffer *copyInspectionLimitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := buffer.maximum - len(buffer.data)
	if remaining <= 0 {
		buffer.overflow = buffer.overflow || len(data) > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.overflow = true
	}
	buffer.data = append(buffer.data, data...)
	return original, nil
}

func writeCopyInspectionManifest(path string, data []byte) error {
	if len(data) < 1 || len(data) > maxArtifactManifestBytes {
		return fmt.Errorf("staged copy completion manifest has an invalid size")
	}
	file, err := privatefile.Create(path)
	if err != nil {
		return fmt.Errorf("create private copy-inspection manifest: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	written, err := io.Copy(file, bytes.NewReader(data))
	if err != nil || written != int64(len(data)) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return fmt.Errorf("write private copy-inspection manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private copy-inspection manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private copy-inspection manifest: %w", err)
	}
	closed = true
	return nil
}
