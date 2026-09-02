package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CopyMode names the machine that owns a transfer. Push jobs run on the
// producer; pull jobs run on the vault. Backup generation remains a separate
// Job and Run lifecycle.
type CopyMode string

const (
	CopyModePush CopyMode = "push"
	CopyModePull CopyMode = "pull"
)

type CopyEndpointKind string

const (
	CopyEndpointLocal  CopyEndpointKind = "local"
	CopyEndpointSSH    CopyEndpointKind = "ssh"
	CopyEndpointSFTP   CopyEndpointKind = "sftp"
	CopyEndpointRclone CopyEndpointKind = "rclone"
)

// CopyEndpoint stores public routing data and references to separately managed
// credentials. Secret key or password material must never be embedded here.
// PinnedHostKey is required for SSH/SFTP so a later transfer implementation
// cannot silently fall back to trust-on-first-use or an insecure callback.
type CopyEndpoint struct {
	Kind          CopyEndpointKind `json:"kind"`
	Location      string           `json:"location,omitempty"`
	CredentialRef string           `json:"credential_ref,omitempty"`
	PinnedHostKey string           `json:"pinned_host_key,omitempty"`
}

type CopyTrigger string

const (
	CopyTriggerManual       CopyTrigger = "manual"
	CopyTriggerAfterSuccess CopyTrigger = "after_success"
	CopyTriggerTimed        CopyTrigger = "timed"
)

// CopyVerificationStrength records what evidence a copy must produce. Values
// are deliberately ordered by verifyCopyStrengthRank, not lexically.
type CopyVerificationStrength string

const (
	CopyVerificationSizeOnly     CopyVerificationStrength = "size-only"
	CopyVerificationSHA256       CopyVerificationStrength = "sha256"
	CopyVerificationSHA256Format CopyVerificationStrength = "sha256+format"
)

type CopyArtifactFilter struct {
	ProducerID string   `json:"producer_id,omitempty"`
	JobID      string   `json:"job_id,omitempty"`
	Formats    []string `json:"formats,omitempty"`
}

// CopyJob is a durable transfer policy. SourceBackupJobID binds an
// after-success push to one local backup stream without conflating the backup
// and copy run histories.
type CopyJob struct {
	ID                       string                   `json:"id"`
	Name                     string                   `json:"name"`
	Enabled                  bool                     `json:"enabled"`
	Mode                     CopyMode                 `json:"mode"`
	Source                   CopyEndpoint             `json:"source"`
	SourceBackupJobID        string                   `json:"source_backup_job_id,omitempty"`
	Destination              CopyEndpoint             `json:"destination"`
	DestinationVolume        *CopyDestinationVolume   `json:"destination_volume,omitempty"`
	ArtifactFilter           CopyArtifactFilter       `json:"artifact_filter,omitempty"`
	Trigger                  CopyTrigger              `json:"trigger"`
	Schedule                 Schedule                 `json:"schedule"`
	ExpectedFreshnessMinutes int                      `json:"expected_freshness_minutes,omitempty"`
	Verification             CopyVerificationStrength `json:"verification"`
	Retention                Retention                `json:"retention"`
	Notification             EmailNotification        `json:"notification,omitempty"`
	TimeoutMinutes           int                      `json:"timeout_minutes"`
	MaxAttempts              int                      `json:"max_attempts"`
	RetryInitialSeconds      int                      `json:"retry_initial_seconds"`
	RetryMaxSeconds          int                      `json:"retry_max_seconds"`
	TransferProofAt          time.Time                `json:"transfer_proof_at,omitempty"`
	TransferProofFingerprint string                   `json:"transfer_proof_fingerprint,omitempty"`
	CreatedAt                time.Time                `json:"created_at"`
	UpdatedAt                time.Time                `json:"updated_at"`
	LastRunAt                time.Time                `json:"last_run_at,omitempty"`
	NextRunAt                time.Time                `json:"next_run_at,omitempty"`
}

// TransferConfigurationFingerprint identifies only the settings that decide
// what bytes are read, where they are published, and how they are verified.
// Scheduling, retention, notification, retry, and display settings are
// intentionally excluded so operational edits do not invalidate a successful
// end-to-end transfer proof.
func (job CopyJob) TransferConfigurationFingerprint() (string, error) {
	sourceBackupJobID := strings.TrimSpace(job.SourceBackupJobID)
	source, err := normalizeCopyEndpoint(job.Source, sourceBackupJobID != "")
	if err != nil {
		return "", fmt.Errorf("copy source: %w", err)
	}
	destination, err := normalizeCopyEndpoint(job.Destination, false)
	if err != nil {
		return "", fmt.Errorf("copy destination: %w", err)
	}
	var volume *CopyDestinationVolume
	if job.DestinationVolume != nil {
		normalized, normalizeErr := normalizeCopyDestinationVolume(*job.DestinationVolume, destination)
		if normalizeErr != nil {
			return "", normalizeErr
		}
		volume = &normalized
	}
	formats := make([]string, 0, len(job.ArtifactFilter.Formats))
	for _, format := range job.ArtifactFilter.Formats {
		formats = append(formats, strings.ToLower(strings.TrimSpace(format)))
	}
	sort.Strings(formats)
	configuration := struct {
		SchemaVersion     int                      `json:"schema_version"`
		Mode              CopyMode                 `json:"mode"`
		Source            CopyEndpoint             `json:"source"`
		SourceBackupJobID string                   `json:"source_backup_job_id,omitempty"`
		Destination       CopyEndpoint             `json:"destination"`
		DestinationVolume *CopyDestinationVolume   `json:"destination_volume,omitempty"`
		ArtifactFilter    CopyArtifactFilter       `json:"artifact_filter"`
		Verification      CopyVerificationStrength `json:"verification"`
	}{
		SchemaVersion: 1,
		Mode:          CopyMode(strings.ToLower(strings.TrimSpace(string(job.Mode)))),
		Source:        source, SourceBackupJobID: sourceBackupJobID,
		Destination: destination, DestinationVolume: volume,
		ArtifactFilter: CopyArtifactFilter{
			ProducerID: strings.TrimSpace(job.ArtifactFilter.ProducerID),
			JobID:      strings.TrimSpace(job.ArtifactFilter.JobID),
			Formats:    formats,
		},
		Verification: CopyVerificationStrength(strings.ToLower(strings.TrimSpace(string(job.Verification)))),
	}
	payload, err := json.Marshal(configuration)
	if err != nil {
		return "", fmt.Errorf("encode copy transfer configuration: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// HasCurrentTransferProof reports whether a real completed transfer has been
// recorded for the job's current transport-critical configuration. A
// read-only endpoint test and an already-present or reconciled artifact never
// create this proof.
func (job CopyJob) HasCurrentTransferProof() bool {
	if job.TransferProofAt.IsZero() || !validCopySHA256(job.TransferProofFingerprint) {
		return false
	}
	fingerprint, err := job.TransferConfigurationFingerprint()
	return err == nil && strings.EqualFold(job.TransferProofFingerprint, fingerprint)
}

type CopyArtifactResult struct {
	ArtifactID       string                   `json:"artifact_id"`
	Source           string                   `json:"source"`
	Destination      string                   `json:"destination"`
	SourceCreatedAt  time.Time                `json:"source_created_at,omitempty"`
	SizeBytes        int64                    `json:"size_bytes"`
	SHA256           string                   `json:"sha256,omitempty"`
	Verification     CopyVerificationStrength `json:"verification"`
	VerifiedAt       time.Time                `json:"verified_at"`
	ManifestPath     string                   `json:"manifest_path,omitempty"`
	ManifestSize     int64                    `json:"manifest_size,omitempty"`
	ManifestSHA256   string                   `json:"manifest_sha256,omitempty"`
	PublicationState ArtifactPublicationState `json:"publication_state"`
	// AlreadyPresent records that this run reverified an immutable destination
	// artifact instead of transferring its bytes. The result can still be the
	// first durable ownership record when a catalog is rebuilt.
	AlreadyPresent bool      `json:"already_present,omitempty"`
	Reconciled     bool      `json:"reconciled,omitempty"`
	PrunedAt       time.Time `json:"pruned_at,omitempty"`
	PruneReason    string    `json:"prune_reason,omitempty"`
}

type CopyRun struct {
	ID                    string                   `json:"id"`
	JobID                 string                   `json:"job_id"`
	Trigger               CopyTrigger              `json:"trigger"`
	Status                RunStatus                `json:"status"`
	StartedAt             time.Time                `json:"started_at"`
	FinishedAt            time.Time                `json:"finished_at,omitempty"`
	RequiredVerification  CopyVerificationStrength `json:"required_verification"`
	Discovered            int                      `json:"discovered,omitempty"`
	AlreadyPresent        int                      `json:"already_present,omitempty"`
	BytesCopied           int64                    `json:"bytes_copied,omitempty"`
	NewestSourceAt        time.Time                `json:"newest_source_at,omitempty"`
	Artifacts             []CopyArtifactResult     `json:"artifacts,omitempty"`
	Warnings              []string                 `json:"warnings,omitempty"`
	RetentionError        string                   `json:"retention_error,omitempty"`
	NotificationAttempted bool                     `json:"notification_attempted,omitempty"`
	NotificationSent      bool                     `json:"notification_sent,omitempty"`
	NotificationError     string                   `json:"notification_error,omitempty"`
	Error                 string                   `json:"error,omitempty"`
}

func (job *CopyJob) ApplyDefaults(now time.Time) error {
	if job == nil {
		return fmt.Errorf("copy job is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(job.ID) == "" {
		id, err := NewID("copy")
		if err != nil {
			return err
		}
		job.ID = id
	}
	job.Name = strings.TrimSpace(job.Name)
	job.Mode = CopyMode(strings.ToLower(strings.TrimSpace(string(job.Mode))))
	job.SourceBackupJobID = strings.TrimSpace(job.SourceBackupJobID)
	job.Trigger = CopyTrigger(strings.ToLower(strings.TrimSpace(string(job.Trigger))))
	if job.Trigger == "" {
		job.Trigger = CopyTriggerManual
	}
	job.Verification = CopyVerificationStrength(strings.ToLower(strings.TrimSpace(string(job.Verification))))
	if job.Verification == "" {
		job.Verification = CopyVerificationSHA256Format
	}
	job.TransferProofFingerprint = strings.ToLower(strings.TrimSpace(job.TransferProofFingerprint))
	if !job.TransferProofAt.IsZero() {
		job.TransferProofAt = job.TransferProofAt.UTC()
	}
	if job.TimeoutMinutes <= 0 {
		job.TimeoutMinutes = DefaultTimeoutMinutes
	}
	job.applyCompatibilityDefaults()
	if job.Retention.KeepLast == 0 && job.Retention.MaxAgeDays == 0 && job.Retention.MaxTotalBytes == 0 {
		job.Retention.KeepLast = 14
	}
	job.Notification.applyDefaults()
	job.ArtifactFilter.ProducerID = strings.TrimSpace(job.ArtifactFilter.ProducerID)
	job.ArtifactFilter.JobID = strings.TrimSpace(job.ArtifactFilter.JobID)
	for index := range job.ArtifactFilter.Formats {
		job.ArtifactFilter.Formats[index] = strings.TrimSpace(job.ArtifactFilter.Formats[index])
	}
	var err error
	job.Source, err = normalizeCopyEndpoint(job.Source, job.SourceBackupJobID != "")
	if err != nil {
		return fmt.Errorf("copy source: %w", err)
	}
	job.Destination, err = normalizeCopyEndpoint(job.Destination, false)
	if err != nil {
		return fmt.Errorf("copy destination: %w", err)
	}
	if job.DestinationVolume != nil {
		volume, volumeErr := normalizeCopyDestinationVolume(*job.DestinationVolume, job.Destination)
		if volumeErr != nil {
			return volumeErr
		}
		job.DestinationVolume = &volume
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now.UTC()
	}
	job.UpdatedAt = now.UTC()
	return job.Validate()
}

// applyCompatibilityDefaults fills fields added after the first copy catalog
// schema. Stored job JSON is intentionally forward-compatible, so older rows
// decode these values as zero and must receive the same safe defaults as a
// newly created job before validation or execution.
func (job *CopyJob) applyCompatibilityDefaults() {
	if job == nil {
		return
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 3
	}
	if job.RetryInitialSeconds == 0 {
		job.RetryInitialSeconds = 2
	}
	if job.RetryMaxSeconds == 0 {
		job.RetryMaxSeconds = 60
	}
}

func (job CopyJob) Validate() error {
	if strings.TrimSpace(job.Name) == "" {
		return fmt.Errorf("copy job name is required")
	}
	switch job.Mode {
	case CopyModePush, CopyModePull:
	default:
		return fmt.Errorf("unsupported copy mode %q (use push or pull)", job.Mode)
	}
	source, err := normalizeCopyEndpoint(job.Source, strings.TrimSpace(job.SourceBackupJobID) != "")
	if err != nil {
		return fmt.Errorf("copy source: %w", err)
	}
	if source != job.Source {
		return fmt.Errorf("copy source must use its normalized form")
	}
	destination, err := normalizeCopyEndpoint(job.Destination, false)
	if err != nil {
		return fmt.Errorf("copy destination: %w", err)
	}
	if destination != job.Destination {
		return fmt.Errorf("copy destination must use its normalized form")
	}
	if job.DestinationVolume != nil {
		if err := validateNormalizedCopyDestinationVolume(*job.DestinationVolume, destination); err != nil {
			return err
		}
	}
	if source.Kind == destination.Kind && source.Location != "" && source.Location == destination.Location {
		return fmt.Errorf("copy source and destination must be different")
	}
	if job.Mode == CopyModePush && source.Kind != CopyEndpointLocal {
		return fmt.Errorf("push copy source must be local to the producer")
	}
	if job.Mode == CopyModePull && destination.Kind != CopyEndpointLocal {
		return fmt.Errorf("pull copy destination must be local to the vault")
	}
	if job.SourceBackupJobID != "" && source.Kind != CopyEndpointLocal {
		return fmt.Errorf("source backup job can only be used with a local source")
	}
	if job.SourceBackupJobID != "" && job.ArtifactFilter.JobID != "" && job.ArtifactFilter.JobID != job.SourceBackupJobID {
		return fmt.Errorf("copy artifact job filter %q conflicts with source backup job %q", job.ArtifactFilter.JobID, job.SourceBackupJobID)
	}
	if hasUnsafeCopyText(job.SourceBackupJobID) {
		return fmt.Errorf("source backup job ID contains an unsupported control character")
	}

	switch job.Trigger {
	case CopyTriggerManual:
		if job.Schedule.Kind != "" && job.Schedule.Kind != ScheduleManual {
			return fmt.Errorf("manual copy trigger cannot also have a timed schedule")
		}
	case CopyTriggerAfterSuccess:
		if job.Mode != CopyModePush {
			return fmt.Errorf("after-success copy trigger is only valid for push jobs")
		}
		if strings.TrimSpace(job.SourceBackupJobID) == "" {
			return fmt.Errorf("after-success copy trigger requires a source backup job ID")
		}
		if job.Schedule.Kind != "" && job.Schedule.Kind != ScheduleManual {
			return fmt.Errorf("after-success copy trigger cannot also have a timed schedule")
		}
	case CopyTriggerTimed:
		if job.Schedule.Kind == "" || job.Schedule.Kind == ScheduleManual {
			return fmt.Errorf("timed copy trigger requires an interval, daily, or weekly schedule")
		}
	default:
		return fmt.Errorf("unsupported copy trigger %q", job.Trigger)
	}
	if err := job.Schedule.Validate(); err != nil {
		return err
	}
	if job.ExpectedFreshnessMinutes < 0 || job.ExpectedFreshnessMinutes > 5*365*24*60 {
		return fmt.Errorf("expected freshness must be between 0 and five years")
	}
	if _, ok := verifyCopyStrengthRank(job.Verification); !ok {
		return fmt.Errorf("unsupported copy verification strength %q", job.Verification)
	}
	if job.TransferProofAt.IsZero() != (job.TransferProofFingerprint == "") {
		return fmt.Errorf("copy transfer proof requires both its timestamp and configuration fingerprint")
	}
	if job.TransferProofFingerprint != "" && !validCopySHA256(job.TransferProofFingerprint) {
		return fmt.Errorf("copy transfer proof fingerprint must contain 64 hexadecimal characters")
	}
	if job.Retention.KeepLast < 0 || job.Retention.MaxAgeDays < 0 || job.Retention.MaxTotalBytes < 0 {
		return fmt.Errorf("copy retention values cannot be negative")
	}
	if err := job.Notification.Validate(); err != nil {
		return err
	}
	if job.TimeoutMinutes < 1 || job.TimeoutMinutes > 24*60 {
		return fmt.Errorf("copy timeout must be between 1 and 1440 minutes")
	}
	if job.MaxAttempts < 1 || job.MaxAttempts > 10 {
		return fmt.Errorf("copy attempts must be between 1 and 10")
	}
	if job.RetryInitialSeconds < 1 || job.RetryInitialSeconds > 3600 || job.RetryMaxSeconds < job.RetryInitialSeconds || job.RetryMaxSeconds > 24*60*60 {
		return fmt.Errorf("copy retry delay must start between 1 and 3600 seconds and cap between the initial delay and 86400 seconds")
	}
	if err := validateCopyArtifactFilter(job.ArtifactFilter); err != nil {
		return err
	}
	return nil
}

func normalizeCopyEndpoint(endpoint CopyEndpoint, allowJobReference bool) (CopyEndpoint, error) {
	endpoint.Kind = CopyEndpointKind(strings.ToLower(strings.TrimSpace(string(endpoint.Kind))))
	endpoint.Location = strings.TrimSpace(endpoint.Location)
	endpoint.CredentialRef = strings.TrimSpace(endpoint.CredentialRef)
	endpoint.PinnedHostKey = strings.TrimSpace(endpoint.PinnedHostKey)
	if hasUnsafeCopyText(endpoint.Location) || hasUnsafeCopyText(endpoint.CredentialRef) || hasUnsafeCopyText(endpoint.PinnedHostKey) {
		return CopyEndpoint{}, fmt.Errorf("endpoint contains an unsupported control character")
	}
	switch endpoint.Kind {
	case CopyEndpointLocal:
		if endpoint.Location == "" {
			if !allowJobReference {
				return CopyEndpoint{}, fmt.Errorf("local path is required")
			}
		} else {
			absolute, err := filepath.Abs(filepath.Clean(endpoint.Location))
			if err != nil || !filepath.IsAbs(absolute) {
				return CopyEndpoint{}, fmt.Errorf("local path must be absolute")
			}
			endpoint.Location = absolute
		}
		if endpoint.CredentialRef != "" || endpoint.PinnedHostKey != "" {
			return CopyEndpoint{}, fmt.Errorf("local endpoint cannot contain SSH credentials or a host key")
		}
	case CopyEndpointRclone:
		if endpoint.Location == "" {
			return CopyEndpoint{}, fmt.Errorf("rclone location is required")
		}
		normalized, err := NormalizeBackupDestination(endpoint.Location)
		if err != nil || !IsRemoteBackupDestination(normalized) {
			return CopyEndpoint{}, fmt.Errorf("rclone location must use rclone://remote/path")
		}
		endpoint.Location = normalized
		if endpoint.PinnedHostKey != "" {
			return CopyEndpoint{}, fmt.Errorf("rclone endpoint cannot contain an SSH host key")
		}
	case CopyEndpointSSH, CopyEndpointSFTP:
		if endpoint.Location == "" {
			return CopyEndpoint{}, fmt.Errorf("%s location is required", endpoint.Kind)
		}
		parsed, err := url.Parse(endpoint.Location)
		if err != nil {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP location must use ssh:// or sftp://[user@]host/absolute/path")
		}
		scheme := strings.ToLower(parsed.Scheme)
		if (scheme != string(CopyEndpointSSH) && scheme != string(CopyEndpointSFTP)) || parsed.Hostname() == "" || !strings.HasPrefix(parsed.Path, "/") {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP location must use ssh:// or sftp://[user@]host/absolute/path")
		}
		if parsed.User != nil {
			if hasUnsafeCopyText(parsed.User.Username()) {
				return CopyEndpoint{}, fmt.Errorf("SSH/SFTP username contains an unsupported control character")
			}
			if _, present := parsed.User.Password(); present {
				return CopyEndpoint{}, fmt.Errorf("SSH/SFTP passwords must use a credential reference, not the endpoint URL")
			}
		}
		if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint requires an explicit service username")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP location cannot contain a query or fragment")
		}
		port := parsed.Port()
		if port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return CopyEndpoint{}, fmt.Errorf("SSH/SFTP port must be between 1 and 65535")
			}
			if number == 22 {
				port = ""
			} else {
				port = strconv.Itoa(number)
			}
		}
		decodedPath, err := url.PathUnescape(parsed.EscapedPath())
		if err != nil || hasUnsafeCopyText(decodedPath) || copyPathTraverses(decodedPath) {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP path must not contain parent traversal")
		}
		cleanedPath := path.Clean(decodedPath)
		if cleanedPath == "/" || cleanedPath == "." {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP path must identify a directory below the remote root")
		}
		parsed.Path = cleanedPath
		parsed.RawPath = ""
		parsed.Scheme = string(CopyEndpointSFTP)
		endpoint.Kind = CopyEndpointSFTP
		hostname := canonicalCopyEndpointHostname(parsed.Hostname())
		if port != "" {
			parsed.Host = net.JoinHostPort(hostname, port)
		} else if strings.Contains(hostname, ":") {
			parsed.Host = "[" + hostname + "]"
		} else {
			parsed.Host = hostname
		}
		endpoint.Location = parsed.String()
		if endpoint.CredentialRef == "" {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint requires a credential reference")
		}
		if endpoint.PinnedHostKey == "" {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint requires a pinned host key")
		}
		canonicalPin, err := canonicalSFTPFingerprint(endpoint.PinnedHostKey)
		if err != nil {
			return CopyEndpoint{}, err
		}
		endpoint.PinnedHostKey = canonicalPin
		if !filepath.IsAbs(endpoint.CredentialRef) {
			return CopyEndpoint{}, fmt.Errorf("SSH/SFTP credential reference must be an absolute private identity path")
		}
		endpoint.CredentialRef = filepath.Clean(endpoint.CredentialRef)
	default:
		return CopyEndpoint{}, fmt.Errorf("unsupported copy endpoint kind %q", endpoint.Kind)
	}
	return endpoint, nil
}

func canonicalCopyEndpointHostname(hostname string) string {
	host := hostname
	zone := ""
	if before, after, found := strings.Cut(host, "%"); found {
		host, zone = before, after
	}
	if address := net.ParseIP(host); address != nil {
		host = address.String()
	} else {
		host = strings.ToLower(host)
	}
	if zone != "" {
		return host + "%" + zone
	}
	return host
}

func validateCopyArtifactFilter(filter CopyArtifactFilter) error {
	if hasUnsafeCopyText(filter.ProducerID) || hasUnsafeCopyText(filter.JobID) {
		return fmt.Errorf("copy artifact filter contains an unsupported control character")
	}
	seen := make(map[string]struct{}, len(filter.Formats))
	for _, format := range filter.Formats {
		if format == "" || hasUnsafeCopyText(format) {
			return fmt.Errorf("copy artifact formats must be non-empty and contain no control characters")
		}
		key := strings.ToLower(format)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("copy artifact format %q is duplicated", format)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (result CopyArtifactResult) validate(required CopyVerificationStrength, requireComplete bool) error {
	if strings.TrimSpace(result.ArtifactID) == "" || hasUnsafeCopyText(result.ArtifactID) {
		return fmt.Errorf("copied artifact ID is required")
	}
	if strings.TrimSpace(result.Source) == "" || strings.TrimSpace(result.Destination) == "" ||
		hasUnsafeCopyText(result.Source) || hasUnsafeCopyText(result.Destination) {
		return fmt.Errorf("copied artifact source and destination are required")
	}
	if result.SizeBytes < 1 {
		return fmt.Errorf("copied artifact size must be positive")
	}
	actualRank, ok := verifyCopyStrengthRank(result.Verification)
	if !ok {
		return fmt.Errorf("unsupported copied artifact verification strength %q", result.Verification)
	}
	requiredRank, ok := verifyCopyStrengthRank(required)
	if !ok {
		return fmt.Errorf("unsupported required copy verification strength %q", required)
	}
	if actualRank < requiredRank {
		return fmt.Errorf("copied artifact verification %q is weaker than required %q", result.Verification, required)
	}
	if actualRank >= 2 {
		digest := strings.TrimSpace(result.SHA256)
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256DigestBytes {
			return fmt.Errorf("copied artifact SHA-256 must contain 64 hexadecimal characters")
		}
	} else if strings.TrimSpace(result.SHA256) != "" && !validCopySHA256(result.SHA256) {
		return fmt.Errorf("copied artifact SHA-256 must contain 64 hexadecimal characters")
	}
	if result.VerifiedAt.IsZero() {
		return fmt.Errorf("copied artifact verification time is required")
	}
	if result.PrunedAt.IsZero() && strings.TrimSpace(result.PruneReason) != "" {
		return fmt.Errorf("copied artifact prune reason requires a prune time")
	}
	if !result.PrunedAt.IsZero() && strings.TrimSpace(result.PruneReason) == "" {
		return fmt.Errorf("copied artifact prune time requires a reason")
	}
	switch result.PublicationState {
	case ArtifactPublicationComplete:
		if strings.TrimSpace(result.ManifestPath) == "" || hasUnsafeCopyText(result.ManifestPath) || result.ManifestSize < 1 {
			return fmt.Errorf("completed copied artifact requires a published manifest")
		}
		if !validCopySHA256(result.ManifestSHA256) {
			return fmt.Errorf("copied artifact manifest SHA-256 must contain 64 hexadecimal characters")
		}
	case ArtifactPublicationArtifactOnly, ArtifactPublicationUncertain:
		if requireComplete {
			return fmt.Errorf("successful copy run cannot contain %q publication state", result.PublicationState)
		}
		if strings.TrimSpace(result.ManifestSHA256) != "" && !validCopySHA256(result.ManifestSHA256) {
			return fmt.Errorf("copied artifact manifest SHA-256 must contain 64 hexadecimal characters")
		}
	default:
		return fmt.Errorf("unsupported copied artifact publication state %q", result.PublicationState)
	}
	return nil
}

func validCopySHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256DigestBytes
}

func verifyCopyStrengthRank(strength CopyVerificationStrength) (int, bool) {
	switch strength {
	case CopyVerificationSizeOnly:
		return 1, true
	case CopyVerificationSHA256:
		return 2, true
	case CopyVerificationSHA256Format:
		return 3, true
	default:
		return 0, false
	}
}

func copyPathTraverses(value string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func hasUnsafeCopyText(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

const sha256DigestBytes = 32
