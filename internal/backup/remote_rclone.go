package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"time"
)

var findRcloneTool = exec.LookPath

type rcloneObject struct {
	Path    string `json:"Path"`
	Name    string `json:"Name"`
	Size    int64  `json:"Size"`
	ModTime string `json:"ModTime"`
	IsDir   bool   `json:"IsDir"`
}

func requireRclone() (string, error) {
	if tool, err := findRcloneTool("rclone"); err == nil {
		return tool, nil
	}
	for _, candidate := range clientToolCandidates(runtime.GOOS, "rclone") {
		if info, err := os.Stat(candidate); err == nil && usableClientToolMode(runtime.GOOS, info.Mode()) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("rclone was not found; install rclone, run `rclone config`, and ensure it is available to the dbterm backup agent")
}

func runRclone(ctx context.Context, stdout io.Writer, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tool, err := requireRclone()
	if err != nil {
		return err
	}
	commandArgs := append([]string{"--ask-password=false"}, args...)
	command := exec.CommandContext(ctx, tool, commandArgs...)
	command.Stdout = stdout
	stderr := &restoreTailBuffer{limit: restoreOutputTailBytes}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return fmt.Errorf("rclone %s failed: %w", nonEmptyRcloneAction(args), err)
		}
		return fmt.Errorf("rclone %s failed: %s", nonEmptyRcloneAction(args), detail)
	}
	return nil
}

func nonEmptyRcloneAction(args []string) string {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "operation"
	}
	return args[0]
}

func ensureRcloneDestination(ctx context.Context, destination destinationSpec) error {
	if destination.kind != destinationRclone {
		return fmt.Errorf("rclone destination is required")
	}
	if err := runRclone(ctx, io.Discard, "mkdir", destination.rclonePath()); err != nil {
		return fmt.Errorf("prepare remote backup destination %s: %w", destination.String(), err)
	}
	return nil
}

func cleanupStaleRclonePublicationPartials(ctx context.Context, destination destinationSpec, olderThan time.Time) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if destination.kind != destinationRclone {
		return 0, fmt.Errorf("rclone destination is required")
	}
	var output bytes.Buffer
	if err := runRclone(ctx, &output, "lsjson", destination.rclonePath(), "--max-depth", "1", "--files-only", "--no-mimetype"); err != nil {
		return 0, fmt.Errorf("list remote backup staging objects: %w", err)
	}
	var items []rcloneObject
	if err := json.Unmarshal(output.Bytes(), &items); err != nil {
		return 0, fmt.Errorf("decode remote backup staging listing: %w", err)
	}
	removed := 0
	for _, item := range items {
		name := item.Name
		if name == "" {
			name = item.Path
		}
		if !isRcloneUploadPartialName(name) {
			continue
		}
		modified, err := time.Parse(time.RFC3339Nano, item.ModTime)
		if err != nil || !modified.Before(olderThan) {
			continue
		}
		partial := destination
		partial.remotePath = path.Join(destination.remotePath, name)
		if err := deleteRcloneArtifact(ctx, partial); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func isRcloneUploadPartialName(name string) bool {
	const prefix = ".dbterm-upload_"
	const suffix = ".partial"
	if path.Base(name) != name || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(identifier) != 24 {
		return false
	}
	decoded, err := hex.DecodeString(identifier)
	return err == nil && len(decoded) == 12
}

func inspectRcloneObject(ctx context.Context, object destinationSpec) (rcloneObject, bool, error) {
	if object.kind != destinationRclone || object.remotePath == "" {
		return rcloneObject{}, false, fmt.Errorf("remote backup artifact path is required")
	}
	var output bytes.Buffer
	err := runRclone(ctx, &output, "lsjson", object.rclonePath(), "--stat", "--no-mimetype")
	if err == nil {
		var item rcloneObject
		if decodeErr := json.Unmarshal(output.Bytes(), &item); decodeErr != nil {
			return rcloneObject{}, false, fmt.Errorf("decode rclone object metadata: %w", decodeErr)
		}
		if item.IsDir {
			return rcloneObject{}, false, nil
		}
		return item, true, nil
	}

	// Some object-store backends represent a missing object as an empty virtual
	// directory, while filesystem-like remotes return an error. Ask the parent
	// for the exact generated basename so a genuine missing object is portable
	// across both behaviours without treating authentication failures as absence.
	parent := object
	parent.remotePath = path.Dir(object.remotePath)
	if parent.remotePath == "." {
		parent.remotePath = ""
	}
	name := path.Base(object.remotePath)
	output.Reset()
	listErr := runRclone(ctx, &output, "lsjson", parent.rclonePath(), "--max-depth", "1", "--files-only", "--no-mimetype")
	if listErr != nil {
		return rcloneObject{}, false, err
	}
	var items []rcloneObject
	if decodeErr := json.Unmarshal(output.Bytes(), &items); decodeErr != nil {
		return rcloneObject{}, false, fmt.Errorf("decode rclone destination listing: %w", decodeErr)
	}
	for _, item := range items {
		if item.Name == name || item.Path == name {
			return item, true, nil
		}
	}
	return rcloneObject{}, false, nil
}

func publishRcloneNoReplace(ctx context.Context, stagedPath string, object destinationSpec, size int64, progress ProgressFunc) error {
	// Keep this low-level guard even though Job.Validate rejects rclone backup
	// destinations. Future callers must not accidentally re-enable a weaker
	// check-then-move protocol and label it immutable.
	return ErrRcloneBackupPublicationDisabled
}

func verifyRcloneArtifactForPrune(ctx context.Context, object destinationSpec, artifact Artifact) (bool, error) {
	initial, exists, err := inspectRcloneObject(ctx, object)
	if err != nil || !exists {
		return exists, err
	}
	if artifact.Size > 0 && initial.Size != artifact.Size {
		return false, fmt.Errorf("retention refused changed remote artifact %s: size is %d, catalog recorded %d", object.String(), initial.Size, artifact.Size)
	}
	if strings.TrimSpace(artifact.SHA256) == "" {
		return true, nil
	}
	hash := sha256.New()
	if err := runRclone(ctx, hash, "cat", object.rclonePath()); err != nil {
		return false, fmt.Errorf("verify remote artifact %s before deletion: %w", object.String(), err)
	}
	if digest := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(digest, strings.TrimSpace(artifact.SHA256)) {
		return false, fmt.Errorf("retention refused changed remote artifact %s: SHA-256 no longer matches the catalog", object.String())
	}
	after, stillExists, err := inspectRcloneObject(ctx, object)
	if err != nil {
		return false, err
	}
	if !stillExists || after.Size != initial.Size || (initial.ModTime != "" && after.ModTime != initial.ModTime) {
		return false, fmt.Errorf("retention refused remote artifact that changed during verification: %s", object.String())
	}
	return true, nil
}

func deleteRcloneArtifact(ctx context.Context, object destinationSpec) error {
	if err := runRclone(ctx, io.Discard, "deletefile", object.rclonePath()); err != nil {
		return fmt.Errorf("remove expired remote backup %s: %w", object.String(), err)
	}
	return nil
}

func parseRemoteArtifactWithin(root destinationSpec, raw string) (destinationSpec, error) {
	object, err := parseDestination(raw)
	if err != nil {
		return destinationSpec{}, err
	}
	if root.kind != destinationRclone || object.kind != destinationRclone || root.remoteName != object.remoteName {
		return destinationSpec{}, fmt.Errorf("retention refused remote path outside destination: %s", raw)
	}
	relative := object.remotePath
	if root.remotePath != "" {
		prefix := root.remotePath + "/"
		if !strings.HasPrefix(object.remotePath, prefix) {
			return destinationSpec{}, fmt.Errorf("retention refused remote path outside destination: %s", raw)
		}
		relative = strings.TrimPrefix(object.remotePath, prefix)
	}
	if relative == "" || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || path.IsAbs(relative) {
		return destinationSpec{}, fmt.Errorf("retention refused remote path outside destination: %s", raw)
	}
	return object, nil
}
