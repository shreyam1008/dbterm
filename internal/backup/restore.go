package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

type RestoreMode string

const (
	RestoreModeMerge RestoreMode = "merge"
	RestoreModeClean RestoreMode = "clean"

	// Short aliases keep call sites readable while retaining the enum-style
	// names used by the other backup model types.
	RestoreMerge = RestoreModeMerge
	RestoreClean = RestoreModeClean
)

type RestoreOptions struct {
	Mode              RestoreMode
	StopOnError       bool
	SingleTransaction bool
	AgeIdentityPath   string
	// MaxDecodedBytes limits each decoded wrapper layer. Zero uses
	// DefaultMaxDecodedBytes.
	MaxDecodedBytes int64
	// FileSetTargets opts individual bundle file sets into restore. An empty
	// slice deliberately means database-only restore. Source roots are never
	// stored in a bundle, so every selected set requires an explicit new root.
	FileSetTargets []RestoreFileSetTarget
	// OverwriteFileSetFiles permits atomic replacement of existing regular
	// files. Symbolic links, reparse points, and non-regular files are always
	// refused. The zero value is no-clobber.
	OverwriteFileSetFiles bool
	// MaxFileSetFiles and MaxFileSetBytes limit the selected file-set payload.
	// Zero uses the conservative package defaults.
	MaxFileSetFiles int64
	MaxFileSetBytes int64
}

// RestoreFileSetTarget maps one named bundle file set to a new, explicitly
// selected root. Bundles intentionally do not carry their original absolute
// roots, so restore can never silently write back to a production path.
type RestoreFileSetTarget struct {
	Label string
	Root  string
}

// RestoreFileSetPlan is the immutable, normalized file publication contract
// shown during restore review.
type RestoreFileSetPlan struct {
	Label     string
	Root      string
	FileCount int64
	SizeBytes int64
}

type RestorePlan struct {
	Inspection       *Inspection
	Target           config.ConnectionConfig
	Options          RestoreOptions
	IncludedFileSets []ManifestFileSet
	FileSetTargets   []RestoreFileSetPlan
	Warnings         []string
}

// BuildRestorePlan performs every check that does not require opening the
// artifact or connecting to the target. ExecuteRestore repeats this validation
// so a caller cannot bypass it by mutating or hand-building a plan.
func BuildRestorePlan(inspection *Inspection, target *config.ConnectionConfig, options RestoreOptions) (*RestorePlan, error) {
	if inspection == nil {
		return nil, fmt.Errorf("backup inspection is required before restore")
	}
	if target == nil {
		return nil, fmt.Errorf("restore target connection is required")
	}
	if _, err := configuredMaxDecodedBytes(options.MaxDecodedBytes); err != nil {
		return nil, err
	}
	if inspection.Locked || inspection.Confidence == ConfidenceLocked {
		return nil, fmt.Errorf("backup is encrypted and locked; inspect it with the age identity before planning a restore")
	}
	if inspection.Format == FormatUnknown {
		return nil, fmt.Errorf("backup format is unknown; restore was not planned")
	}
	if inspection.Format == FormatGenericSQL || inspection.Confidence == ConfidenceAmbiguous {
		return nil, fmt.Errorf("SQL dialect is ambiguous; select and verify a database-specific backup before restore")
	}
	if inspection.Confidence != ConfidenceExact && inspection.Confidence != ConfidenceStrong {
		return nil, fmt.Errorf("backup inspection confidence %q is not sufficient for restore", inspection.Confidence)
	}

	databaseFormat, err := databaseRestoreFormat(inspection)
	if err != nil {
		return nil, err
	}
	expectedEngine, err := engineForRestoreFormat(databaseFormat)
	if err != nil {
		return nil, err
	}
	if inspection.Engine != expectedEngine {
		return nil, fmt.Errorf("backup inspection is inconsistent: format %s requires engine %s, not %s", inspection.Format, expectedEngine, inspection.Engine)
	}
	if target.Type != expectedEngine {
		return nil, fmt.Errorf("restore engine mismatch: %s backup cannot be restored into %s", expectedEngine, target.Type)
	}
	if len(inspection.Wrappers) > maxWrapperDepth {
		return nil, fmt.Errorf("backup has %d wrappers; at most %d are supported", len(inspection.Wrappers), maxWrapperDepth)
	}
	for _, wrapper := range inspection.Wrappers {
		switch wrapper {
		case WrapperGzip, WrapperZstd, WrapperZip:
		case WrapperAge:
			if strings.TrimSpace(options.AgeIdentityPath) == "" {
				return nil, fmt.Errorf("age identity file is required to restore this encrypted backup")
			}
		default:
			return nil, fmt.Errorf("unsupported backup wrapper %q", wrapper)
		}
	}
	if strings.TrimSpace(inspection.Path) == "" {
		return nil, fmt.Errorf("inspected backup path is missing")
	}
	if inspection.Size <= 0 {
		return nil, fmt.Errorf("inspected backup size must be positive")
	}
	digest, err := hex.DecodeString(inspection.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return nil, fmt.Errorf("inspected backup SHA-256 is invalid")
	}

	if options.Mode == "" {
		// A zero-value plan is deliberately conservative. Callers that expose the
		// toggles explicitly pass a non-empty mode and retain their bool choices.
		options.Mode = RestoreModeMerge
		options.StopOnError = true
		options.SingleTransaction = true
	}
	if options.Mode != RestoreModeMerge && options.Mode != RestoreModeClean {
		return nil, fmt.Errorf("invalid restore mode %q; choose merge or clean", options.Mode)
	}

	targetCopy := *target
	if err := normalizeAndValidateRestoreTarget(&targetCopy); err != nil {
		return nil, err
	}
	if databaseFormat == FormatSQLiteDatabase {
		if options.Mode != RestoreModeClean {
			return nil, fmt.Errorf("SQLite database-file restore requires explicit clean mode because it replaces the target database atomically")
		}
		backupPath, backupErr := filepath.Abs(filepath.Clean(inspection.Path))
		if backupErr == nil && filepath.Clean(backupPath) == filepath.Clean(targetCopy.FilePath) {
			return nil, fmt.Errorf("SQLite backup and restore target resolve to the same path; choose a separate target")
		}
	}

	fileSetTargets, normalizedTargetOptions, err := buildRestoreFileSetPlan(inspection, options)
	if err != nil {
		return nil, err
	}
	options = normalizedTargetOptions
	inspectionCopy := cloneInspection(inspection)
	plan := &RestorePlan{
		Inspection:       inspectionCopy,
		Target:           targetCopy,
		Options:          options,
		IncludedFileSets: cloneManifestFileSets(inspection.FileSets),
		FileSetTargets:   fileSetTargets,
	}
	plan.Warnings = restorePlanWarnings(databaseFormat, options)
	plan.Warnings = append(plan.Warnings, restoreFileSetWarnings(plan)...)
	return plan, nil
}

func ExecuteRestore(ctx context.Context, plan *RestorePlan, emit func(string)) error {
	if plan == nil {
		return fmt.Errorf("restore plan is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	validated, err := BuildRestorePlan(plan.Inspection, &plan.Target, plan.Options)
	if err != nil {
		return err
	}
	emitRestore(emit, "Verifying the backup checksum and content")
	payload, err := materializeRestorePayload(ctx, validated.Inspection, validated.Options)
	if err != nil {
		return err
	}
	defer payload.cleanup()
	if err := preflightRestoreFilePublications(payload.restoreFiles, validated.Options.OverwriteFileSetFiles, validated.Inspection.Path, validated.Target.FilePath); err != nil {
		return err
	}

	emitRestore(emit, "Backup verified; starting restore")
	databaseFormat, err := databaseRestoreFormat(validated.Inspection)
	if err != nil {
		return err
	}
	switch databaseFormat {
	case FormatPostgresCustom, FormatPostgresTar:
		err = executePostgresArchiveRestore(ctx, validated, payload.path, emit)
	case FormatPostgresSQL:
		err = executePostgresSQLRestore(ctx, validated, payload.path, emit)
	case FormatMySQLSQL:
		err = executeMySQLRestore(ctx, validated, payload.path, emit)
	case FormatSQLiteDatabase:
		err = executeSQLiteDatabaseRestore(ctx, validated, payload, emit)
	case FormatSQLiteSQL:
		err = executeSQLiteSQLRestore(ctx, validated, payload, emit)
	default:
		err = fmt.Errorf("restore is not implemented for format %s", validated.Inspection.Format)
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("restore stopped: %w", ctx.Err())
		}
		return redactRestoreError(err, &validated.Target)
	}
	if len(payload.restoreFiles) > 0 {
		emitRestore(emit, "Database restore completed; publishing explicitly selected file sets")
		if err := publishRestoredFileSets(ctx, payload.restoreFiles, validated.Options.OverwriteFileSetFiles, emit); err != nil {
			return fmt.Errorf("database restore succeeded, but bundled file publication failed: %w", err)
		}
	}
	emitRestore(emit, "Restore completed successfully")
	return nil
}

func databaseRestoreFormat(inspection *Inspection) (Format, error) {
	if inspection == nil {
		return FormatUnknown, fmt.Errorf("backup inspection is required before restore")
	}
	if inspection.Format != FormatDBTermBundle {
		return inspection.Format, nil
	}
	if inspection.DatabaseFormat == "" || inspection.DatabaseFormat == FormatUnknown || inspection.DatabaseFormat == FormatDBTermBundle {
		return FormatUnknown, fmt.Errorf("dbterm bundle inspection is missing a supported embedded database format")
	}
	return inspection.DatabaseFormat, nil
}

func engineForRestoreFormat(format Format) (config.DBType, error) {
	switch format {
	case FormatPostgresCustom, FormatPostgresTar, FormatPostgresSQL:
		return config.PostgreSQL, nil
	case FormatMySQLSQL:
		return config.MySQL, nil
	case FormatSQLiteDatabase, FormatSQLiteSQL:
		return config.SQLite, nil
	case FormatGenericSQL:
		return "", fmt.Errorf("generic SQL must be classified before restore")
	case FormatUnknown:
		return "", fmt.Errorf("unknown backup format cannot be restored")
	default:
		return "", fmt.Errorf("unsupported backup format %q", format)
	}
}

func normalizeAndValidateRestoreTarget(target *config.ConnectionConfig) error {
	if target == nil {
		return fmt.Errorf("restore target connection is required")
	}
	if target.ReadOnly {
		return fmt.Errorf("restore target %q is marked read-only; edit the saved connection before restoring", target.Name)
	}
	switch target.Type {
	case config.PostgreSQL, config.MySQL:
		target.Host = strings.TrimSpace(target.Host)
		target.Port = strings.TrimSpace(target.Port)
		target.User = strings.TrimSpace(target.User)
		target.Database = strings.TrimSpace(target.Database)
		if target.Host == "" {
			target.Host = "localhost"
		}
		if target.Port == "" {
			target.Port = defaultPort(target)
		}
		if target.User == "" {
			return fmt.Errorf("%s restore target is missing a user name", target.TypeLabel())
		}
		if target.Database == "" {
			return fmt.Errorf("%s restore target is missing a database name", target.TypeLabel())
		}
		for label, value := range map[string]string{
			"host": target.Host, "port": target.Port, "user": target.User, "database": target.Database,
		} {
			if err := validateRestoreField(label, value); err != nil {
				return err
			}
		}
		port, err := strconv.Atoi(target.Port)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("restore target port must be a number from 1 to 65535")
		}
		if target.Type == config.PostgreSQL {
			if strings.ContainsAny(target.Password, "\r\n") {
				return fmt.Errorf("PostgreSQL password cannot contain a line break when used with a private pgpass file")
			}
			if err := validatePostgresSSLMode(target.SSLMode); err != nil {
				return err
			}
		}
	case config.SQLite:
		if strings.TrimSpace(target.FilePath) == "" {
			return fmt.Errorf("SQLite restore target file path is required")
		}
		if strings.IndexByte(target.FilePath, 0) >= 0 {
			return fmt.Errorf("SQLite restore target file path contains a NUL byte")
		}
		absolute, err := filepath.Abs(filepath.Clean(target.FilePath))
		if err != nil {
			return fmt.Errorf("resolve SQLite restore target %q: %w", target.FilePath, err)
		}
		target.FilePath = absolute
	default:
		return fmt.Errorf("restore target type %q is not supported", target.Type)
	}
	return nil
}

func validateRestoreField(label, value string) error {
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("restore target %s contains a NUL byte", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("restore target %s contains a control character", label)
		}
	}
	return nil
}

func validatePostgresSSLMode(value string) error {
	mode := strings.TrimSpace(value)
	if mode == "" {
		return nil
	}
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("unsupported PostgreSQL SSL mode %q", value)
	}
}

func cloneInspection(source *Inspection) *Inspection {
	if source == nil {
		return nil
	}
	copyValue := *source
	copyValue.Wrappers = append([]Wrapper(nil), source.Wrappers...)
	copyValue.Evidence = append([]string(nil), source.Evidence...)
	copyValue.Warnings = append([]string(nil), source.Warnings...)
	copyValue.RequiredTools = append([]string(nil), source.RequiredTools...)
	copyValue.FileSets = cloneManifestFileSets(source.FileSets)
	return &copyValue
}

func cloneManifestFileSets(source []ManifestFileSet) []ManifestFileSet {
	if source == nil {
		return nil
	}
	result := make([]ManifestFileSet, len(source))
	for index := range source {
		result[index] = source[index]
		result[index].ChangedFiles = append([]string(nil), source[index].ChangedFiles...)
		result[index].Warnings = append([]string(nil), source[index].Warnings...)
	}
	return result
}

func restorePlanWarnings(format Format, options RestoreOptions) []string {
	var warnings []string
	if options.Mode == RestoreModeMerge {
		warnings = append(warnings, "Merge mode will not pre-emptively remove existing target objects; name or data conflicts can still stop the restore.")
		if format == FormatSQLiteSQL {
			warnings = append(warnings, "SQLite merge mode applies SQL to a staged copy of a consistent target snapshot; the live target is replaced only after integrity checks pass.")
		}
	} else {
		switch format {
		case FormatPostgresCustom, FormatPostgresTar:
			warnings = append(warnings, "Clean mode is explicit: objects represented in the archive will be dropped before they are recreated.")
		case FormatPostgresSQL, FormatMySQLSQL:
			warnings = append(warnings, "For a plain SQL backup, clean behavior is controlled by statements inside the inspected file; dbterm will not drop the target database.")
		case FormatSQLiteDatabase:
			warnings = append(warnings, "The SQLite target file will be atomically replaced only after a verified pre-restore snapshot and staged database check.")
		case FormatSQLiteSQL:
			warnings = append(warnings, "SQLite clean mode builds a new staged database and atomically replaces the target only after the SQL client and integrity checks succeed.")
		}
	}
	if !options.StopOnError && format == FormatSQLiteSQL {
		warnings = append(warnings, "SQLite SQL restore always stops at the first client error so a partial stage can never be published.")
	} else if !options.StopOnError && format != FormatSQLiteDatabase {
		warnings = append(warnings, "Stop-on-error is disabled; a client may continue after a failed statement and leave a partial restore.")
	}
	if !options.SingleTransaction && (format == FormatPostgresCustom || format == FormatPostgresTar || format == FormatPostgresSQL) {
		warnings = append(warnings, "Single-transaction restore is disabled; an error may leave committed changes in the target.")
	}
	if options.SingleTransaction && format == FormatMySQLSQL {
		warnings = append(warnings, "MySQL transaction boundaries are controlled by the dump; dbterm does not wrap the entire script in a synthetic transaction.")
	}
	return warnings
}

func materializeRestorePayload(ctx context.Context, inspection *Inspection, options RestoreOptions) (*payloadSource, error) {
	maxDecoded, err := configuredMaxDecodedBytes(options.MaxDecodedBytes)
	if err != nil {
		return nil, err
	}
	current, err := snapshotRestoreArtifact(ctx, inspection)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*payloadSource, error) {
		current.cleanup()
		return nil, err
	}

	actualWrappers := make([]Wrapper, 0, len(inspection.Wrappers))
	for depth := 0; ; depth++ {
		wrapper, armored, err := detectWrapper(current)
		if err != nil {
			return fail(err)
		}
		if wrapper == "" {
			break
		}
		if depth >= maxWrapperDepth {
			return fail(fmt.Errorf("backup wrapper nesting exceeds the maximum depth of %d", maxWrapperDepth))
		}
		actualWrappers = append(actualWrappers, wrapper)
		next, err := decodeWrapper(ctx, current, wrapper, armored, InspectOptions{
			AgeIdentityPath: options.AgeIdentityPath,
			MaxDecodedBytes: maxDecoded,
		}, maxDecoded)
		if err != nil {
			return fail(err)
		}
		current.cleanup()
		current = next
	}
	if !slices.Equal(actualWrappers, inspection.Wrappers) {
		return fail(fmt.Errorf("backup wrappers changed since inspection: detected %v, expected %v", actualWrappers, inspection.Wrappers))
	}
	if inspection.Format == FormatDBTermBundle {
		database, err := materializeDBTermBundleRestore(ctx, current, inspection, options, maxDecoded)
		if err != nil {
			return fail(err)
		}
		current.cleanup()
		current = database
	}
	format, engine, _, _, _, err := detectPayload(ctx, current)
	if err != nil {
		return fail(err)
	}
	expectedFormat, err := databaseRestoreFormat(inspection)
	if err != nil {
		return fail(err)
	}
	if format != expectedFormat || engine != inspection.Engine {
		return fail(fmt.Errorf("backup content changed since inspection: detected %s/%s, expected %s/%s", format, engine, expectedFormat, inspection.Engine))
	}
	return current, nil
}

func snapshotRestoreArtifact(ctx context.Context, inspection *Inspection) (*payloadSource, error) {
	path, info, err := resolveInspectionPath(inspection.Path)
	if err != nil {
		return nil, err
	}
	if info.Size() != inspection.Size {
		return nil, fmt.Errorf("backup size changed since inspection: found %d bytes, expected %d", info.Size(), inspection.Size)
	}
	source, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open backup for restore: %w", err)
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect backup for restore: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() != inspection.Size {
		return nil, fmt.Errorf("backup changed before restore materialization")
	}

	temporary, err := privatefile.CreateTemp("", "dbterm-restore-source-", "")
	if err != nil {
		return nil, fmt.Errorf("create private restore staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	fail := func(err error) (*payloadSource, error) {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), &contextReader{ctx: ctx, reader: source})
	if err != nil {
		return fail(fmt.Errorf("materialize backup for restore: %w", err))
	}
	if written != inspection.Size {
		return fail(fmt.Errorf("backup size changed during restore materialization: copied %d bytes, expected %d", written, inspection.Size))
	}
	afterCopy, err := source.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect backup after restore materialization: %w", err))
	}
	if afterCopy.Size() != openedInfo.Size() || afterCopy.ModTime() != openedInfo.ModTime() {
		return fail(fmt.Errorf("backup changed during restore materialization"))
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actualDigest, inspection.SHA256) {
		return fail(fmt.Errorf("backup checksum changed since inspection: found %s, expected %s", actualDigest, inspection.SHA256))
	}
	if err := temporary.Sync(); err != nil {
		return fail(fmt.Errorf("sync private restore staging file: %w", err))
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind private restore staging file: %w", err))
	}
	return &payloadSource{file: temporary, path: temporaryPath, size: written, temporary: true}, nil
}

func emitRestore(emit func(string), message string) {
	if emit != nil {
		emit(message)
	}
}

func redactRestoreError(err error, target *config.ConnectionConfig) error {
	if err == nil || target == nil {
		return err
	}
	message := err.Error()
	for _, secret := range []string{target.Password, target.AuthToken} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return fmt.Errorf("%s", message)
}
