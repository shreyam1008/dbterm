package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func backupFileSetCommand(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printBackupFileSetHelp()
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		return backupFileSetListCommand(args[1:])
	case "add":
		return backupFileSetAddCommand(args[1:])
	case "remove", "delete":
		return backupFileSetRemoveCommand(args[1:])
	default:
		return fmt.Errorf("unknown backup files command %q (run `dbterm backup files --help`)", args[0])
	}
}

func backupFileSetListCommand(args []string) error {
	fs := flag.NewFlagSet("backup files list", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dbterm backup files list [--json] <job-id-or-name>")
	}
	store, err := openBackupCLIStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(job.FileSets)
	}
	if len(job.FileSets) == 0 {
		fmt.Printf("Backup %q has no application file sets. It contains only the engine-native database backup.\n", job.Name)
		return nil
	}
	for _, set := range job.FileSets {
		policy := "required"
		if !set.Required {
			policy = "optional"
		}
		fmt.Printf("%-24s  %-8s  %s\n  include %s\n", set.Label, policy, set.Root, strings.Join(set.Include, ", "))
		if len(set.Exclude) > 0 {
			fmt.Printf("  exclude %s\n", strings.Join(set.Exclude, ", "))
		}
	}
	return nil
}

func backupFileSetAddCommand(args []string) error {
	fs := flag.NewFlagSet("backup files add", flag.ContinueOnError)
	label := fs.String("label", "", "portable file-set label")
	root := fs.String("root", "", "absolute application folder")
	optional := fs.Bool("optional", false, "warn and omit this set if a safe best-effort capture cannot be completed")
	replace := fs.Bool("replace", false, "replace an existing set with the same label")
	var includes repeatStringFlag
	var excludes repeatStringFlag
	fs.Var(&includes, "include", "slash-separated glob; repeat for multiple patterns (default **)")
	fs.Var(&excludes, "exclude", "slash-separated glob; repeat for multiple patterns")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(*label) == "" || strings.TrimSpace(*root) == "" {
		return fmt.Errorf("usage: dbterm backup files add --label NAME --root ABSOLUTE_FOLDER [--include GLOB] [--exclude GLOB] [--optional] [--replace] <job-id-or-name>")
	}
	set, err := buildCLIFileSet(*label, *root, includes, excludes, !*optional)
	if err != nil {
		return err
	}
	store, err := openBackupCLIStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	replaced := false
	for index := range job.FileSets {
		if strings.EqualFold(job.FileSets[index].Label, set.Label) {
			if !*replace {
				return fmt.Errorf("file set %q already exists; use --replace to change it", set.Label)
			}
			job.FileSets[index] = set
			replaced = true
			break
		}
	}
	if !replaced {
		job.FileSets = append(job.FileSets, set)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		return err
	}
	verb := "added"
	if replaced {
		verb = "replaced"
	}
	fmt.Printf("Application file set %s: %s\nRoot: %s\nPolicy: %s\n\nFuture backups use a private best-effort live-folder capture with change detection and a dbterm bundle. Existing artifacts were not changed.\n", verb, set.Label, set.Root, map[bool]string{true: "required", false: "optional"}[set.Required])
	return nil
}

func backupFileSetRemoveCommand(args []string) error {
	fs := flag.NewFlagSet("backup files remove", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm removal from future backup policy")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 2 || !*yes {
		return fmt.Errorf("usage: dbterm backup files remove --yes <job-id-or-name> <label> (existing backup bundles are never changed)")
	}
	store, err := openBackupCLIStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	label := strings.TrimSpace(fs.Arg(1))
	remaining := make([]backupcore.FileSet, 0, len(job.FileSets))
	found := false
	for _, set := range job.FileSets {
		if strings.EqualFold(set.Label, label) {
			found = true
			continue
		}
		remaining = append(remaining, set)
	}
	if !found {
		return fmt.Errorf("file set %q was not found on backup %q", label, job.Name)
	}
	job.FileSets = remaining
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		return err
	}
	fmt.Printf("Application file set %q removed from future runs of %q. Existing backup artifacts were not changed.\n", label, job.Name)
	return nil
}

func buildCLIFileSet(label, root string, includes, excludes []string, required bool) (backupcore.FileSet, error) {
	expanded, err := resolveBackupCLIPath(root)
	if err != nil {
		return backupcore.FileSet{}, fmt.Errorf("resolve file-set root: %w", err)
	}
	if !filepath.IsAbs(expanded) {
		return backupcore.FileSet{}, fmt.Errorf("file-set root must be absolute")
	}
	if len(includes) == 0 {
		includes = []string{"**"}
	}
	job := backupcore.Job{
		Name: "file-set validation", ConnectionID: "validation", Destination: filepath.Clean(expanded),
		Compression: backupcore.CompressionZstd, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual},
		FileSets: []backupcore.FileSet{{Label: strings.TrimSpace(label), Root: filepath.Clean(expanded), Include: append([]string(nil), includes...), Exclude: append([]string(nil), excludes...), Required: required}},
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		return backupcore.FileSet{}, err
	}
	return job.FileSets[0], nil
}

func openBackupCLIStore() (*backupcore.Store, error) {
	return backupcore.OpenDefaultStore()
}

func printBackupFileSetHelp() {
	fmt.Print(`
  dbterm backup files — include application-owned folders with a database recovery point

  USAGE
    dbterm backup files list [--json] <job-id|name>
    dbterm backup files add --label photos --root ABSOLUTE_FOLDER [options] <job-id|name>
    dbterm backup files remove --yes <job-id|name> <label>

  OPTIONS
    --include GLOB   Repeatable slash-separated pattern; default **
    --exclude GLOB   Repeatable slash-separated exclusion
    --optional       Warn and atomically omit the whole set if it cannot be captured
    --replace        Replace the same named file-set policy

  File roots stay local to the producer catalog and are never serialized.
  Symlinks, reparse points, path escapes, and non-regular files are refused.
  A required set fails the backup; an optional set is omitted with a portable warning.
  Adding file sets changes future output to a self-contained dbterm bundle. Database-only
  jobs keep their existing engine-native artifact and restore compatibility.

`)
}
