package backup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"filippo.io/age"
)

const (
	DefaultFilenameTemplate = "{connection}_{engine}_{date}_{time}_{run}"
	DefaultTimeoutMinutes   = 30
)

// ErrRcloneBackupPublicationDisabled is returned for generation jobs that
// target a generic rclone remote. rclone's single-object move operation is not
// an atomic create-if-absent primitive across its provider ecosystem, so it
// cannot uphold dbterm's immutable publication contract under a race.
var ErrRcloneBackupPublicationDisabled = errors.New("rclone backup publication is disabled: generic rclone finalization cannot guarantee atomic create-only publication; use an absolute local or mounted destination, then copy the completed artifact separately")

// ErrRcloneRetentionDisabled prevents a check-then-delete race where a remote
// object could be replaced after verification but before generic rclone
// deletefile removes it. Safe remote retention needs a backend generation or
// ETag-aware conditional delete operation.
var ErrRcloneRetentionDisabled = errors.New("rclone backup retention is disabled: generic rclone cannot conditionally delete the exact remote object version that dbterm verified; use a backend-specific version-aware retention policy")

type Compression string

const (
	CompressionNone Compression = "none"
	CompressionGzip Compression = "gzip"
	CompressionZip  Compression = "zip"
	CompressionZstd Compression = "zstd"
)

type Encryption string

const (
	EncryptionNone Encryption = "none"
	EncryptionAge  Encryption = "age"
)

type NotificationPolicy string

const (
	NotificationNever   NotificationPolicy = "never"
	NotificationFailure NotificationPolicy = "failure"
	NotificationSuccess NotificationPolicy = "success"
	NotificationBoth    NotificationPolicy = "both"
)

type SMTPTLSMode string

const (
	SMTPTLSStartTLS SMTPTLSMode = "starttls"
	SMTPTLSImplicit SMTPTLSMode = "implicit"
	SMTPTLSNone     SMTPTLSMode = "none"
)

// EmailNotification is stored in the private backup catalog as part of the
// job JSON. Password is necessarily plaintext so the unattended OS service
// can authenticate; the SQLite catalog and its parent state directory are
// protected with 0600/0700 permissions and must be treated as secrets.
type EmailNotification struct {
	Policy     NotificationPolicy `json:"policy"`
	SMTPHost   string             `json:"smtp_host,omitempty"`
	SMTPPort   int                `json:"smtp_port,omitempty"`
	TLSMode    SMTPTLSMode        `json:"tls_mode,omitempty"`
	Recipients []string           `json:"recipients,omitempty"`
	Username   string             `json:"username,omitempty"`
	Password   string             `json:"password,omitempty"`
	From       string             `json:"from,omitempty"`
}

type Trigger string

const (
	TriggerManual    Trigger = "manual"
	TriggerScheduled Trigger = "scheduled"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

type ArtifactPublicationState string

const (
	ArtifactPublicationComplete     ArtifactPublicationState = "complete"
	ArtifactPublicationArtifactOnly ArtifactPublicationState = "artifact-only"
	ArtifactPublicationUncertain    ArtifactPublicationState = "uncertain"
)

// Job is a durable backup policy. It references a saved connection by its
// stable ID so renaming or reordering dashboard entries cannot redirect it.
type Job struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ConnectionID     string            `json:"connection_id"`
	Enabled          bool              `json:"enabled"`
	Destination      string            `json:"destination"`
	FilenameTemplate string            `json:"filename_template"`
	Compression      Compression       `json:"compression"`
	CompressionLevel int               `json:"compression_level"`
	Encryption       Encryption        `json:"encryption"`
	AgeRecipient     string            `json:"age_recipient,omitempty"`
	Schedule         Schedule          `json:"schedule"`
	Retention        Retention         `json:"retention"`
	Notification     EmailNotification `json:"notification,omitempty"`
	TimeoutMinutes   int               `json:"timeout_minutes"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	LastRunAt        time.Time         `json:"last_run_at,omitempty"`
	NextRunAt        time.Time         `json:"next_run_at,omitempty"`
}

type Retention struct {
	KeepLast      int   `json:"keep_last"`
	MaxAgeDays    int   `json:"max_age_days"`
	MaxTotalBytes int64 `json:"max_total_bytes,omitempty"`
}

type Artifact struct {
	ID                string                   `json:"id,omitempty"`
	Path              string                   `json:"path"`
	Size              int64                    `json:"size"`
	SHA256            string                   `json:"sha256"`
	Format            string                   `json:"format"`
	Verified          bool                     `json:"verified"`
	VerificationLevel string                   `json:"verification_level,omitempty"`
	PublicationState  ArtifactPublicationState `json:"publication_state,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	BackupName        string                   `json:"backup_name,omitempty"`
	ManifestPath      string                   `json:"manifest_path,omitempty"`
	ManifestSize      int64                    `json:"manifest_size,omitempty"`
	ManifestSHA256    string                   `json:"manifest_sha256,omitempty"`
	PrunedAt          time.Time                `json:"pruned_at,omitempty"`
	PruneReason       string                   `json:"prune_reason,omitempty"`
}

type Run struct {
	ID                    string    `json:"id"`
	JobID                 string    `json:"job_id"`
	Trigger               Trigger   `json:"trigger"`
	Status                RunStatus `json:"status"`
	StartedAt             time.Time `json:"started_at"`
	FinishedAt            time.Time `json:"finished_at,omitempty"`
	Artifact              Artifact  `json:"artifact,omitempty"`
	Error                 string    `json:"error,omitempty"`
	RetentionError        string    `json:"retention_error,omitempty"`
	NotificationAttempted bool      `json:"notification_attempted,omitempty"`
	NotificationSent      bool      `json:"notification_sent,omitempty"`
	NotificationError     string    `json:"notification_error,omitempty"`
}

func NewID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	prefix = strings.Trim(strings.ToLower(prefix), "-_")
	if prefix == "" {
		return hex.EncodeToString(raw[:]), nil
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

func (j *Job) ApplyDefaults(now time.Time) error {
	if j == nil {
		return fmt.Errorf("backup job is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(j.ID) == "" {
		id, err := NewID("job")
		if err != nil {
			return err
		}
		j.ID = id
	}
	j.Name = strings.TrimSpace(j.Name)
	j.ConnectionID = strings.TrimSpace(j.ConnectionID)
	j.Destination = strings.TrimSpace(j.Destination)
	j.FilenameTemplate = strings.TrimSpace(j.FilenameTemplate)
	if j.FilenameTemplate == "" {
		j.FilenameTemplate = DefaultFilenameTemplate
	}
	if j.Compression == "" {
		j.Compression = CompressionZstd
	}
	if j.Encryption == "" {
		j.Encryption = EncryptionNone
	}
	if j.TimeoutMinutes <= 0 {
		j.TimeoutMinutes = DefaultTimeoutMinutes
	}
	if j.Retention.KeepLast == 0 && j.Retention.MaxAgeDays == 0 && j.Retention.MaxTotalBytes == 0 {
		j.Retention.KeepLast = 14
	}
	j.Notification.applyDefaults()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now.UTC()
	}
	j.UpdatedAt = now.UTC()
	return j.Validate()
}

func (j Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("job name is required")
	}
	if strings.TrimSpace(j.ConnectionID) == "" {
		return fmt.Errorf("saved connection is required")
	}
	if strings.TrimSpace(j.Destination) == "" {
		return fmt.Errorf("backup destination is required")
	}
	normalizedDestination, err := NormalizeBackupDestination(j.Destination)
	if err != nil {
		return err
	}
	if normalizedDestination != j.Destination {
		return fmt.Errorf("backup destination must be normalized as %q", normalizedDestination)
	}
	if IsRemoteBackupDestination(normalizedDestination) {
		return ErrRcloneBackupPublicationDisabled
	}
	if strings.ContainsAny(j.FilenameTemplate, `/\\`) {
		return fmt.Errorf("filename template must not contain path separators")
	}
	switch j.Compression {
	case CompressionNone, CompressionGzip, CompressionZip, CompressionZstd:
	default:
		return fmt.Errorf("unsupported compression %q", j.Compression)
	}
	if j.CompressionLevel < 0 || j.CompressionLevel > 22 {
		return fmt.Errorf("compression level must be between 0 and 22")
	}
	if (j.Compression == CompressionGzip || j.Compression == CompressionZip) && j.CompressionLevel > 9 {
		return fmt.Errorf("%s compression level must be between 0 (default) and 9", j.Compression)
	}
	switch j.Encryption {
	case EncryptionNone:
	case EncryptionAge:
		if strings.TrimSpace(j.AgeRecipient) == "" {
			return fmt.Errorf("age encryption requires an age1… recipient")
		}
		if _, err := age.ParseX25519Recipient(strings.TrimSpace(j.AgeRecipient)); err != nil {
			return fmt.Errorf("age encryption requires a valid X25519 age1… recipient: %w", err)
		}
	default:
		return fmt.Errorf("unsupported encryption %q", j.Encryption)
	}
	if j.Retention.KeepLast < 0 || j.Retention.MaxAgeDays < 0 || j.Retention.MaxTotalBytes < 0 {
		return fmt.Errorf("retention values cannot be negative")
	}
	if err := j.Notification.Validate(); err != nil {
		return err
	}
	if j.TimeoutMinutes < 1 || j.TimeoutMinutes > 24*60 {
		return fmt.Errorf("timeout must be between 1 and 1440 minutes")
	}
	return j.Schedule.Validate()
}
