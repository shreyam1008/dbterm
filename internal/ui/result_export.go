package ui

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	pageResultExport         = "resultExport"
	pageResultExportProgress = "resultExportProgress"
	resultExportProgressStep = 100
	resultExportRunning      = uint32(0)
	resultExportCanceling    = uint32(1)
	resultExportFinished     = uint32(2)
)

type resultExportScope uint8

const (
	resultExportSelectedRows resultExportScope = iota
	resultExportCurrentPage
	resultExportAllMatching
)

type resultExportScopeOption struct {
	scope resultExportScope
	label string
}

type resultExportSnapshot struct {
	headers []string
	rows    [][]string
}

type resultExportPlan struct {
	scope        resultExportScope
	scopeLabel   string
	outputPath   string
	expectedRows int
	snapshot     resultExportSnapshot
	db           *sql.DB
	query        string
	queryArgs    []any
}

type resultExportCSVProducer func(context.Context, *csv.Writer, func(int)) (int, error)

type resultExportCleanupError struct {
	cause      error
	path       string
	cleanupErr error
}

func (e *resultExportCleanupError) Error() string {
	return fmt.Sprintf("%v (could not remove %s: %v)", e.cause, e.path, e.cleanupErr)
}

func (e *resultExportCleanupError) Unwrap() error { return e.cause }

// exportCurrentResultsToCSV opens an explicit scope/path picker. The actual
// file work always happens in a worker so large table exports never block the
// tview event loop.
func (a *App) exportCurrentResultsToCSV() {
	if a == nil {
		return
	}
	if a.currentResultRowCount() == 0 {
		a.ShowAlert(fmt.Sprintf("%s No result rows to export.\n\nRun a query or load a table first.", iconInfo), "main")
		return
	}
	if a.isQueryRunning() {
		a.ShowAlert(fmt.Sprintf("%s A query is still running.\n\nWait for it to finish, or press Esc to cancel it, before choosing an export scope.", iconInfo), "main")
		return
	}
	if a.isImportRunning() {
		a.ShowAlert(fmt.Sprintf("%s A SQL import is still running.\n\nFinish or cancel the import before exporting results.", iconInfo), "main")
		return
	}
	if a.pages.HasPage(pageResultExportProgress) {
		return
	}

	returnFocus := a.app.GetFocus()
	options := a.resultExportScopeOptions()
	optionLabels := make([]string, len(options))
	for index, option := range options {
		optionLabels[index] = option.label
	}

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Export Results to CSV ", iconResults)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetFieldTextColor(text).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	scopeInput := tview.NewDropDown().
		SetLabel("Scope").
		SetOptions(optionLabels, nil).
		SetCurrentOption(0)
	form.AddFormItem(scopeInput)

	pathInput := tview.NewInputField().
		SetLabel("CSV Path").
		SetText(a.defaultResultExportPath()).
		SetFieldWidth(72).
		SetPlaceholder("/path/to/results.csv")
	form.AddFormItem(pathInput)

	closeModal := func() {
		a.pages.RemovePage(pageResultExport)
		a.restoreResultExportFocus(returnFocus)
	}
	startExport := func() {
		optionIndex, _ := scopeInput.GetCurrentOption()
		if optionIndex < 0 || optionIndex >= len(options) {
			a.ShowAlert(fmt.Sprintf("%s Choose which rows to export.", iconInfo), pageResultExport)
			return
		}

		outputPath, err := resolveResultExportPath(pathInput.GetText())
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid CSV destination:\n\n%v", iconWarn, err), pageResultExport)
			return
		}
		plan, err := a.prepareResultExportPlan(options[optionIndex], outputPath)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not prepare CSV export:\n\n%v", iconWarn, err), pageResultExport)
			return
		}

		a.pages.RemovePage(pageResultExport)
		a.runResultExport(plan, returnFocus)
	}

	pathInput.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			startExport()
		}
	})
	form.AddButton("Export", startExport)
	form.AddButton("Cancel", closeModal)
	form.SetCancelFunc(closeModal)
	form.SetFocus(0)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Tab / Shift+Tab[-] Move  │  [yellow]Enter[-] Choose / Export  │  [yellow]Esc[-] Cancel ")
	footer.SetBackgroundColor(crust)

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)
	modalW, modalH := a.modalSize(72, 108, 11, 15)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageResultExport, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) resultExportScopeOptions() []resultExportScopeOption {
	rowCount := a.currentResultRowCount()
	options := make([]resultExportScopeOption, 0, 3)
	if selected := a.selectedResultRowCount(); selected > 0 {
		options = append(options, resultExportScopeOption{
			scope: resultExportSelectedRows,
			label: fmt.Sprintf("Selected rows (%d)", selected),
		})
	}
	options = append(options, resultExportScopeOption{
		scope: resultExportCurrentPage,
		label: fmt.Sprintf("Current displayed page (%d)", rowCount),
	})
	if a.db != nil && a.isTableResultActive() {
		label := "All matching table rows"
		if a.totalRowCount >= 0 {
			label = fmt.Sprintf("%s (%d)", label, a.totalRowCount)
		}
		options = append(options, resultExportScopeOption{
			scope: resultExportAllMatching,
			label: label,
		})
	}
	return options
}

func (a *App) prepareResultExportPlan(option resultExportScopeOption, outputPath string) (resultExportPlan, error) {
	plan := resultExportPlan{
		scope:      option.scope,
		scopeLabel: option.label,
		outputPath: outputPath,
	}

	switch option.scope {
	case resultExportSelectedRows:
		rows := a.selectedResultRows()
		if len(rows) == 0 {
			return resultExportPlan{}, fmt.Errorf("no rows are selected")
		}
		snapshot, err := a.captureResultExportSnapshot(rows)
		if err != nil {
			return resultExportPlan{}, err
		}
		plan.snapshot = snapshot
		plan.expectedRows = len(snapshot.rows)
	case resultExportCurrentPage:
		rowCount := a.currentResultRowCount()
		rows := make([]int, rowCount)
		for index := range rows {
			rows[index] = index + 1
		}
		snapshot, err := a.captureResultExportSnapshot(rows)
		if err != nil {
			return resultExportPlan{}, err
		}
		plan.snapshot = snapshot
		plan.expectedRows = len(snapshot.rows)
	case resultExportAllMatching:
		query, args, err := a.allMatchingResultExportQuery()
		if err != nil {
			return resultExportPlan{}, err
		}
		plan.db = a.db
		plan.query = query
		plan.queryArgs = args
		plan.expectedRows = a.totalRowCount
	default:
		return resultExportPlan{}, fmt.Errorf("unknown export scope")
	}

	return plan, nil
}

// captureResultExportSnapshot converts references to complete strings while
// still on the UI goroutine. The worker never reads the live tview.Table.
func (a *App) captureResultExportSnapshot(rowIndexes []int) (resultExportSnapshot, error) {
	if a == nil || a.results == nil || a.results.GetColumnCount() == 0 {
		return resultExportSnapshot{}, fmt.Errorf("result columns are unavailable")
	}

	columnCount := a.results.GetColumnCount()
	snapshot := resultExportSnapshot{
		headers: make([]string, columnCount),
		rows:    make([][]string, 0, len(rowIndexes)),
	}
	for column := 0; column < columnCount; column++ {
		snapshot.headers[column] = resultExportHeaderText(a.results.GetCell(0, column))
	}

	for _, row := range rowIndexes {
		if row <= 0 || row >= a.results.GetRowCount() {
			return resultExportSnapshot{}, fmt.Errorf("result row %d is no longer available", row)
		}
		record := make([]string, columnCount)
		for column := 0; column < columnCount; column++ {
			record[column] = resultExportCellText(a.results.GetCell(row, column))
		}
		snapshot.rows = append(snapshot.rows, record)
	}
	if len(snapshot.rows) == 0 {
		return resultExportSnapshot{}, fmt.Errorf("no result data available")
	}
	return snapshot, nil
}

func resultExportHeaderText(cell *tview.TableCell) string {
	if cell == nil {
		return ""
	}
	if reference, ok := cell.GetReference().(string); ok && reference != "" {
		return reference
	}
	return stripSortIndicator(cell.Text)
}

// resultExportCellText is deliberately reference-first: TableCell.Text is a
// preview and can be truncated or numerically rounded. Legacy references that
// predate rawValue still retain the full string in value.
func resultExportCellText(cell *tview.TableCell) string {
	if cell == nil {
		return ""
	}
	switch reference := cell.GetReference().(type) {
	case resultCellReference:
		return resultExportReferenceText(reference)
	case *resultCellReference:
		if reference != nil {
			return resultExportReferenceText(*reference)
		}
	}
	return cell.Text
}

func resultExportReferenceText(reference resultCellReference) string {
	if reference.isNull {
		return fullCellValue(nil)
	}
	if reference.rawValue != nil {
		return fullCellValueForDatabaseType(reference.rawValue, reference.databaseType)
	}
	return reference.value
}

func (a *App) allMatchingResultExportQuery() (string, []any, error) {
	if a == nil || a.db == nil || !a.isTableResultActive() || strings.TrimSpace(a.selectedTable) == "" {
		return "", nil, fmt.Errorf("all-matching export requires an active table")
	}

	query := fmt.Sprintf("SELECT * FROM %s", quoteIdentifier(a.dbType, a.selectedTable))
	var args []any
	if filter := a.activeResultFilter(a.selectedTable); filter != nil {
		clause, filterArgs := resultFilterSQL(a.dbType, filter)
		query += clause
		args = append(args, filterArgs...)
	}
	if sortColumn := a.serverSortColumnName(); sortColumn != "" {
		direction := "ASC"
		if !a.sortAsc {
			direction = "DESC"
		}
		query = fmt.Sprintf("%s ORDER BY %s %s", query, quoteIdentifier(a.dbType, sortColumn), direction)
	}
	return query, args, nil
}

func (a *App) runResultExport(plan resultExportPlan, returnFocus tview.Primitive) {
	ctx, cancel := context.WithCancel(context.Background())
	var state atomic.Uint32

	modal := tview.NewModal().
		SetText(resultExportProgressText(plan, 0, false)).
		AddButtons([]string{" Cancel "}).
		SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(yellow).
		SetTextColor(text)
	cancelExport := func() {
		if !state.CompareAndSwap(resultExportRunning, resultExportCanceling) {
			return
		}
		cancel()
		modal.SetText(resultExportProgressText(plan, 0, true))
	}
	if !a.beginResultExport(cancelExport) {
		cancel()
		a.ShowAlert(fmt.Sprintf("%s Another CSV export is already running.", iconInfo), "main")
		return
	}
	modal.SetDoneFunc(func(_ int, _ string) {
		cancelExport()
	})

	a.pages.AddPage(pageResultExportProgress, modal, true, true)
	a.app.SetFocus(modal)

	go func() {
		defer cancel()
		defer a.finishResultExport()
		progress := func(rows int) {
			a.queueUpdateDraw(func() {
				if state.Load() != resultExportRunning {
					return
				}
				modal.SetText(resultExportProgressText(plan, rows, false))
			})
		}

		rowsWritten, exportErr := writeResultExportCSVAtomic(ctx, plan.outputPath, plan.csvProducer(), progress)
		canceledByUser := !state.CompareAndSwap(resultExportRunning, resultExportFinished)
		var cancelCleanupErr error
		var cleanupErr *resultExportCleanupError
		canceled := canceledByUser || errors.Is(exportErr, context.Canceled)
		if canceled && errors.As(exportErr, &cleanupErr) {
			cancelCleanupErr = cleanupErr
		}
		if canceledByUser && exportErr == nil {
			if err := os.Remove(plan.outputPath); err != nil && !os.IsNotExist(err) {
				cancelCleanupErr = fmt.Errorf("remove completed CSV after cancellation: %w", err)
			}
		}
		a.queueUpdateDraw(func() {
			a.pages.RemovePage(pageResultExportProgress)
			a.restoreResultExportFocus(returnFocus)

			if cancelCleanupErr != nil {
				a.ShowAlert(fmt.Sprintf("%s CSV export was canceled, but cleanup failed:\n\n%v", iconWarn, cancelCleanupErr), "main")
				return
			}
			if canceled {
				a.flashStatus("[yellow]CSV export canceled; no partial file kept[-]", a.currentResultRowCount(), 1800*time.Millisecond)
				return
			}
			if exportErr != nil {
				a.ShowAlert(fmt.Sprintf("%s CSV export failed:\n\n%v", iconWarn, exportErr), "main")
				return
			}
			a.ShowAlert(fmt.Sprintf("%s CSV export complete.\n\nRows: %d\nFile: %s", iconSuccess, rowsWritten, plan.outputPath), "main")
		})
	}()
}

func (a *App) beginResultExport(cancel context.CancelFunc) bool {
	if a == nil || cancel == nil {
		return false
	}
	a.resultExportMu.Lock()
	defer a.resultExportMu.Unlock()
	if a.resultExportRunning {
		return false
	}
	a.resultExportRunning = true
	a.resultExportCancel = cancel
	return true
}

func (a *App) finishResultExport() {
	if a == nil {
		return
	}
	a.resultExportMu.Lock()
	a.resultExportRunning = false
	a.resultExportCancel = nil
	a.resultExportMu.Unlock()
}

func (a *App) cancelActiveResultExport() bool {
	if a == nil {
		return false
	}
	a.resultExportMu.Lock()
	cancel := a.resultExportCancel
	running := a.resultExportRunning
	a.resultExportMu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

func resultExportProgressText(plan resultExportPlan, rows int, canceling bool) string {
	if canceling {
		return fmt.Sprintf("\n%s Canceling CSV export...\n\nCleaning up the partial file. Please wait.\n\n%s", iconRefresh, tview.Escape(plan.outputPath))
	}
	expected := ""
	if plan.expectedRows >= 0 {
		expected = fmt.Sprintf(" / %d", plan.expectedRows)
	}
	return fmt.Sprintf("\n%s Exporting %s\n\nRows written: %d%s\n\n%s\n\nPress Esc or Cancel to stop safely.",
		iconRefresh, tview.Escape(plan.scopeLabel), rows, expected, tview.Escape(plan.outputPath))
}

func (a *App) restoreResultExportFocus(target tview.Primitive) {
	if a == nil || a.app == nil {
		return
	}
	switch target {
	case a.tables, a.queryInput, a.results:
		if target != nil {
			a.setFocusWithColor(target)
		}
	default:
		if target != nil {
			a.app.SetFocus(target)
		}
	}
}

func (plan resultExportPlan) csvProducer() resultExportCSVProducer {
	if plan.scope == resultExportAllMatching {
		return func(ctx context.Context, writer *csv.Writer, progress func(int)) (int, error) {
			return streamAllMatchingResultCSV(ctx, writer, plan.db, plan.query, plan.queryArgs, progress)
		}
	}
	return func(ctx context.Context, writer *csv.Writer, progress func(int)) (int, error) {
		return writeResultExportSnapshotCSV(ctx, writer, plan.snapshot, progress)
	}
}

func writeResultExportSnapshotCSV(ctx context.Context, writer *csv.Writer, snapshot resultExportSnapshot, progress func(int)) (int, error) {
	if len(snapshot.headers) == 0 {
		return 0, fmt.Errorf("result columns are unavailable")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := writer.Write(snapshot.headers); err != nil {
		return 0, fmt.Errorf("write CSV header: %w", err)
	}

	for index, record := range snapshot.rows {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		if err := writer.Write(record); err != nil {
			return index, fmt.Errorf("write CSV row %d: %w", index+1, err)
		}
		reportResultExportProgress(progress, index+1, index+1 == len(snapshot.rows))
	}
	return len(snapshot.rows), nil
}

func streamAllMatchingResultCSV(ctx context.Context, writer *csv.Writer, db *sql.DB, query string, args []any, progress func(int)) (int, error) {
	if db == nil || strings.TrimSpace(query) == "" {
		return 0, fmt.Errorf("table export query is unavailable")
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("query matching rows: %w", err)
	}
	defer rows.Close()

	headers, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("read result columns: %w", err)
	}
	if len(headers) == 0 {
		return 0, fmt.Errorf("result columns are unavailable")
	}
	databaseTypes := resultDatabaseTypes(rows, len(headers))
	if err := writer.Write(headers); err != nil {
		return 0, fmt.Errorf("write CSV header: %w", err)
	}

	values := make([]any, len(headers))
	valuePointers := make([]any, len(headers))
	for index := range values {
		valuePointers[index] = &values[index]
	}

	rowCount := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return rowCount, err
		}
		if err := rows.Scan(valuePointers...); err != nil {
			return rowCount, fmt.Errorf("scan CSV row %d: %w", rowCount+1, err)
		}
		record := make([]string, len(values))
		for index, value := range values {
			record[index] = fullCellValueForDatabaseType(value, databaseTypes[index])
		}
		if err := writer.Write(record); err != nil {
			return rowCount, fmt.Errorf("write CSV row %d: %w", rowCount+1, err)
		}
		rowCount++
		reportResultExportProgress(progress, rowCount, false)
	}
	if err := rows.Err(); err != nil {
		return rowCount, fmt.Errorf("iterate matching rows: %w", err)
	}
	reportResultExportProgress(progress, rowCount, true)
	return rowCount, nil
}

func reportResultExportProgress(progress func(int), rows int, force bool) {
	if progress != nil && (force || rows%resultExportProgressStep == 0) {
		progress(rows)
	}
}

// writeResultExportCSVAtomic writes beside the destination, fsyncs, and only
// then publishes it without replacing an existing path. Every failure path
// removes the private temporary file.
func writeResultExportCSVAtomic(ctx context.Context, path string, producer resultExportCSVProducer, progress func(int)) (rowCount int, returnErr error) {
	if producer == nil {
		return 0, fmt.Errorf("CSV export source is unavailable")
	}
	if err := ensureResultExportDestinationAvailable(path); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temporary CSV in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
			cause := returnErr
			if cause == nil {
				cause = fmt.Errorf("CSV was published, but temporary-file cleanup failed")
			}
			returnErr = &resultExportCleanupError{cause: cause, path: temporaryPath, cleanupErr: removeErr}
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return 0, fmt.Errorf("secure temporary CSV permissions: %w", err)
	}
	buffered := bufio.NewWriterSize(temporary, 64*1024)
	writer := csv.NewWriter(buffered)
	rowCount, err = producer(ctx, writer, progress)
	if err != nil {
		return rowCount, err
	}
	if err := ctx.Err(); err != nil {
		return rowCount, err
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return rowCount, fmt.Errorf("flush CSV encoder: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		return rowCount, fmt.Errorf("flush CSV file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return rowCount, fmt.Errorf("sync CSV file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return rowCount, fmt.Errorf("close CSV file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return rowCount, err
	}
	if err := publishResultExportNoReplace(ctx, temporaryPath, path); err != nil {
		return rowCount, err
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return rowCount, &resultExportCleanupError{cause: cancelErr, path: path, cleanupErr: removeErr}
		}
		return rowCount, cancelErr
	}
	return rowCount, nil
}

// publishResultExportNoReplace uses an atomic hard link on filesystems that
// support it. FAT/exFAT and some SMB/FUSE mounts do not; there we fall back to
// an exclusive 0600 create-and-copy. The fallback remains no-clobber and
// removes an incomplete destination on every error or cancellation.
func publishResultExportNoReplace(ctx context.Context, temporaryPath, destinationPath string) error {
	linkErr := os.Link(temporaryPath, destinationPath)
	if linkErr == nil {
		return nil
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		return fmt.Errorf("destination already exists: %s (choose a new file name)", destinationPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect CSV destination %s after atomic publish failed: %w", destinationPath, err)
	}
	if err := copyResultExportNoReplace(ctx, temporaryPath, destinationPath); err != nil {
		return fmt.Errorf("publish CSV to %s (atomic link unavailable: %v): %w", destinationPath, linkErr, err)
	}
	return nil
}

func copyResultExportNoReplace(ctx context.Context, sourcePath, destinationPath string) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open completed CSV for portable publication: %w", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create CSV destination without replacing it: %w", err)
	}
	destinationCreated := true
	defer func() {
		_ = destination.Close()
		if returnErr == nil || !destinationCreated {
			return
		}
		if removeErr := os.Remove(destinationPath); removeErr != nil && !os.IsNotExist(removeErr) {
			returnErr = &resultExportCleanupError{cause: returnErr, path: destinationPath, cleanupErr: removeErr}
		}
	}()

	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		readCount, readErr := source.Read(buffer)
		if readCount > 0 {
			written := 0
			for written < readCount {
				writeCount, writeErr := destination.Write(buffer[written:readCount])
				if writeErr != nil {
					return fmt.Errorf("write portable CSV destination: %w", writeErr)
				}
				if writeCount == 0 {
					return io.ErrShortWrite
				}
				written += writeCount
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read completed CSV for portable publication: %w", readErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync portable CSV destination: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close portable CSV destination: %w", err)
	}
	destinationCreated = false
	return nil
}

func resolveResultExportPath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("CSV path is required")
	}
	expanded, err := expandHomePath(path)
	if err != nil {
		return "", err
	}
	if extension := filepath.Ext(expanded); extension == "" {
		expanded += ".csv"
	} else if !strings.EqualFold(extension, ".csv") {
		return "", fmt.Errorf("CSV destination must end in .csv")
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve CSV path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := ensureResultExportDestinationAvailable(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func ensureResultExportDestinationAvailable(path string) error {
	if strings.TrimSpace(path) == "" || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return fmt.Errorf("CSV path must include a file name")
	}
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("access CSV directory %s: %w", directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("CSV parent path is not a directory: %s", directory)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("destination already exists: %s (choose a new file name)", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect CSV destination %s: %w", path, err)
	}
	return nil
}

func (a *App) defaultResultExportPath() string {
	directory, err := os.Getwd()
	if err != nil || strings.TrimSpace(directory) == "" {
		directory = os.TempDir()
	}
	name := "query_results"
	if a != nil && a.isTableResultActive() && strings.TrimSpace(a.selectedTable) != "" {
		name = a.selectedTable
	}
	name = sanitizeResultExportName(name)
	timestamp := time.Now().Format("20060102_150405")
	fileName := fmt.Sprintf("dbterm_%s_%s.csv", name, timestamp)
	path := filepath.Join(directory, fileName)
	for suffix := 2; ; suffix++ {
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return path
		} else if statErr != nil {
			return path
		}
		path = filepath.Join(directory, fmt.Sprintf("dbterm_%s_%s_%d.csv", name, timestamp, suffix))
	}
}

func sanitizeResultExportName(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, character := range strings.TrimSpace(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' {
			builder.WriteRune(character)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	name := strings.Trim(builder.String(), "_")
	if name == "" {
		return "results"
	}
	return name
}
