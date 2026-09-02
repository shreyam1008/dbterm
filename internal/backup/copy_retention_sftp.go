package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

func applySFTPCopyRetention(ctx context.Context, store *Store, job CopyJob, entries []copyRetentionEntry, now time.Time) ([]string, error) {
	parsed, err := parseSFTPCopyEndpoint(job.Destination)
	if err != nil {
		return nil, err
	}
	connection, err := dialSFTPCopyEndpoint(ctx, job.Destination)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if err := requireSFTPDirectory(connection.client, parsed.root); err != nil {
		return nil, fmt.Errorf("copy retention destination: %w", err)
	}
	if err := requireSFTPCreateOnlyPublication(connection.client); err != nil {
		return nil, fmt.Errorf("copy retention requires race-safe capture: %w", err)
	}

	var removed []string
	for index := len(entries) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		entry := entries[index]
		artifactName, err := retainedSFTPArtifactName(job.Destination, entry.Artifact)
		if err != nil {
			return removed, err
		}
		artifactPath, err := safeSFTPChild(parsed.root, artifactName)
		if err != nil {
			return removed, err
		}
		manifestPath, err := safeSFTPChild(parsed.root, artifactName+ArtifactManifestSuffix)
		if err != nil {
			return removed, err
		}

		manifest, manifestExists, err := verifyRecordedSFTPManifest(ctx, connection.client, manifestPath, entry.Artifact)
		if err != nil {
			return removed, err
		}
		artifactExists, err := verifyRecordedSFTPCopyArtifact(ctx, connection.client, artifactPath, entry.Artifact, manifest)
		if err != nil {
			return removed, err
		}
		if manifestExists {
			_, err = captureAndRemoveSFTPPrune(ctx, connection.client, parsed.root, manifestPath,
				entry.Artifact.ManifestSize, entry.Artifact.ManifestSHA256, func(captured string) error {
					_, exists, err := verifyRecordedSFTPManifest(ctx, connection.client, captured, entry.Artifact)
					if err != nil {
						return err
					}
					if !exists {
						return fmt.Errorf("captured completion manifest disappeared")
					}
					return nil
				})
			if err != nil {
				return removed, fmt.Errorf("remove copied SFTP manifest %s: %w", manifestPath, err)
			}
		}
		if artifactExists {
			_, err = captureAndRemoveSFTPPrune(ctx, connection.client, parsed.root, artifactPath,
				entry.Artifact.SizeBytes, entry.Artifact.SHA256, func(captured string) error {
					exists, err := verifyRecordedSFTPCopyArtifact(ctx, connection.client, captured, entry.Artifact, manifest)
					if err != nil {
						return err
					}
					if !exists {
						return fmt.Errorf("captured SFTP artifact disappeared")
					}
					return nil
				})
			if err != nil {
				return removed, fmt.Errorf("remove copied SFTP artifact %s: %w", artifactPath, err)
			}
		}
		reason := "retention"
		if !artifactExists {
			reason = "missing"
		}
		if err := store.MarkCopyArtifactPruned(ctx, entry.RunID, entry.Artifact.ArtifactID, entry.Artifact.Destination, reason, now); err != nil {
			return removed, err
		}
		if artifactExists {
			removed = append(removed, entry.Artifact.Destination)
		}
	}
	return removed, nil
}

func retainedSFTPArtifactName(endpoint CopyEndpoint, artifact CopyArtifactResult) (string, error) {
	parsed, err := url.Parse(artifact.Destination)
	if err != nil {
		return "", fmt.Errorf("copy retention refused malformed SFTP artifact location: %w", err)
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("copy retention refused unsafe SFTP artifact name %q", name)
	}
	if artifact.Destination != sftpDisplayPath(endpoint, name) || artifact.ManifestPath != sftpDisplayPath(endpoint, name+ArtifactManifestSuffix) {
		return "", fmt.Errorf("copy retention refused artifact outside its recorded SFTP destination: %s", artifact.Destination)
	}
	return name, nil
}

func verifyRecordedSFTPManifest(ctx context.Context, client *sftp.Client, manifestPath string, artifact CopyArtifactResult) (*ArtifactManifest, bool, error) {
	if _, err := client.Lstat(manifestPath); err != nil {
		if isSFTPNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect copied SFTP manifest %s: %w", manifestPath, err)
	}
	manifest, size, digest, err := readSFTPArtifactManifest(ctx, client, manifestPath)
	if err != nil {
		return nil, false, fmt.Errorf("verify copied SFTP manifest %s: %w", manifestPath, err)
	}
	if size != artifact.ManifestSize || !strings.EqualFold(digest, artifact.ManifestSHA256) {
		return nil, false, fmt.Errorf("copy retention refused changed SFTP manifest %s", manifestPath)
	}
	if manifest.ArtifactID != artifact.ArtifactID || manifest.SizeBytes != artifact.SizeBytes || !strings.EqualFold(manifest.SHA256, artifact.SHA256) || manifest.Verification != ArtifactVerificationPassed {
		return nil, false, fmt.Errorf("copy retention refused mismatched SFTP manifest %s", manifestPath)
	}
	return manifest, true, nil
}

func verifyRecordedSFTPCopyArtifact(ctx context.Context, client *sftp.Client, artifactPath string, artifact CopyArtifactResult, manifest *ArtifactManifest) (bool, error) {
	if _, err := client.Lstat(artifactPath); err != nil {
		if isSFTPNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect copied SFTP artifact %s: %w", artifactPath, err)
	}
	if manifest != nil {
		if err := verifySFTPArtifact(ctx, client, artifactPath, *manifest, CopyVerificationSHA256Format); err != nil {
			return false, fmt.Errorf("copy retention refused changed SFTP artifact %s: %w", artifactPath, err)
		}
		return true, nil
	}
	if err := verifySFTPPathDigest(ctx, client, artifactPath, artifact.SizeBytes, artifact.SHA256); err != nil {
		return false, fmt.Errorf("copy retention refused changed SFTP artifact %s: %w", artifactPath, err)
	}
	return true, nil
}

func captureAndRemoveSFTPPrune(ctx context.Context, client *sftp.Client, root, source string, size int64, digest string, validate func(string) error) (bool, error) {
	if err := requireSFTPCreateOnlyPublication(client); err != nil {
		return false, err
	}
	quarantine, err := sftpPruneQuarantinePath(root, source, size, digest)
	if err != nil {
		return false, err
	}

	sourceInfo, sourceErr := client.Lstat(source)
	sourceExists := sourceErr == nil
	if sourceErr != nil && !isSFTPNotExist(sourceErr) {
		return false, fmt.Errorf("inspect SFTP retention source %s: %w", source, sourceErr)
	}
	quarantineInfo, quarantineErr := client.Lstat(quarantine)
	quarantineExists := quarantineErr == nil
	if quarantineErr != nil && !isSFTPNotExist(quarantineErr) {
		return false, fmt.Errorf("inspect SFTP retention quarantine %s: %w", quarantine, quarantineErr)
	}
	preserve := func(cause error) (bool, error) {
		return false, fmt.Errorf("copy retention preserved its SFTP capture for manual review at %s: %w", quarantine, cause)
	}
	verifyCapture := func() error {
		if err := verifySFTPPathDigest(ctx, client, quarantine, size, digest); err != nil {
			return err
		}
		if validate != nil {
			return validate(quarantine)
		}
		return nil
	}

	if quarantineExists {
		if quarantineInfo.Mode()&os.ModeSymlink != 0 || !quarantineInfo.Mode().IsRegular() {
			return preserve(fmt.Errorf("retention capture changed type"))
		}
		if err := verifyCapture(); err != nil {
			return preserve(err)
		}
	}
	if !sourceExists {
		if !quarantineExists {
			return false, nil
		}
		if err := client.Remove(quarantine); err != nil && !isSFTPNotExist(err) {
			return preserve(fmt.Errorf("remove verified prior retention capture: %w", err))
		}
		return true, nil
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return false, fmt.Errorf("copy retention refused non-regular SFTP source %s", source)
	}
	if err := verifySFTPPathDigest(ctx, client, source, size, digest); err != nil {
		return false, err
	}

	if !quarantineExists {
		// hardlink@openssh.com is the only widely deployed SFTP primitive that
		// atomically captures a regular file without replacing an existing
		// target. A deterministic, content-bound name also permits safe recovery
		// after a crash between source removal and capture removal.
		if err := client.Link(source, quarantine); err != nil {
			if _, statErr := client.Lstat(quarantine); statErr == nil {
				return false, fmt.Errorf("SFTP retention capture may have completed despite an error; source and capture were preserved for retry: %s and %s: %w", source, quarantine, err)
			}
			return false, preferSFTPContextError(ctx, fmt.Errorf("create race-safe SFTP retention capture: %w", err))
		}
		quarantineExists = true
		if err := verifyCapture(); err != nil {
			return preserve(err)
		}
	}

	// SFTP has no conditional unlink operation. Revalidate the exact owned
	// source immediately before unlinking it; the hard-linked verified capture
	// guarantees recovery bytes remain even if the unlink response is lost.
	if err := verifySFTPPathDigest(ctx, client, source, size, digest); err != nil {
		return preserve(fmt.Errorf("source changed after retention capture: %w", err))
	}
	if err := client.Remove(source); err != nil {
		return preserve(fmt.Errorf("remove captured SFTP source: %w", err))
	}
	if _, err := client.Lstat(source); err == nil {
		return preserve(fmt.Errorf("source still exists after successful remove response"))
	} else if !isSFTPNotExist(err) {
		return preserve(fmt.Errorf("confirm source removal: %w", err))
	}
	if err := verifyCapture(); err != nil {
		return preserve(fmt.Errorf("reverify recovery capture after source removal: %w", err))
	}
	if err := client.Remove(quarantine); err != nil {
		if isSFTPNotExist(err) {
			return true, nil
		}
		return preserve(fmt.Errorf("remove verified SFTP retention capture: %w", err))
	}
	return true, nil
}

func sftpPruneQuarantinePath(root, source string, size int64, digest string) (string, error) {
	identity := sha256.Sum256([]byte(path.Base(source) + "\x00" + fmt.Sprint(size) + "\x00" + strings.ToLower(digest)))
	return safeSFTPChild(root, ".dbterm-prune_"+hex.EncodeToString(identity[:12])+".quarantine")
}

func verifySFTPPathDigest(ctx context.Context, client *sftp.Client, filePath string, expectedSize int64, expectedDigest string) error {
	initial, err := client.Lstat(filePath)
	if err != nil {
		return err
	}
	if err := requireSFTPRegular(initial, filePath, expectedSize); err != nil {
		return err
	}
	file, err := client.Open(filePath)
	if err != nil {
		return preferSFTPContextError(ctx, err)
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.CopyBuffer(hash, &contextReader{ctx: ctx, reader: file}, make([]byte, 256*1024))
	if err != nil {
		return preferSFTPContextError(ctx, err)
	}
	if written != expectedSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedDigest) {
		return fmt.Errorf("SFTP file size or SHA-256 changed: %s", filePath)
	}
	return verifySFTPHandleAndPathUnchanged(client, file, filePath, initial)
}
