// Package d1sql provides dbterm's database/sql adapter for Cloudflare D1.
//
// The upstream cfd1 database/sql adapter builds columns by ranging over a map
// and indexes the first row even when a successful query returns no rows. This
// adapter deliberately uses cfd1's raw endpoint instead: that response carries
// an ordered column slice and represents empty result sets without panicking.
package d1sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"

	"github.com/peterheb/cfd1"
)

// DriverName is intentionally distinct from cfd1's built-in driver name so
// dbterm never reaches the map-backed, empty-result-unsafe adapter.
const DriverName = "dbterm-d1"

func init() {
	sql.Register(DriverName, &dbDriver{})
}

type dsnConfig struct {
	accountID  string
	authToken  string
	databaseID string
}

func parseDSN(raw string) (dsnConfig, error) {
	const maximumDSNBytes = 64 * 1024
	if strings.TrimSpace(raw) == "" || len(raw) > maximumDSNBytes {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 connection string is empty or too large")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return dsnConfig{}, fmt.Errorf("parse Cloudflare D1 connection string")
	}
	if !strings.EqualFold(parsed.Scheme, "d1") || parsed.User == nil || parsed.Host == "" {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 connection string must use d1://account:token@database")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 connection string contains unsupported path, query, fragment, or port data")
	}
	token, hasToken := parsed.User.Password()
	cfg := dsnConfig{
		accountID:  parsed.User.Username(),
		authToken:  token,
		databaseID: parsed.Hostname(),
	}
	if cfg.accountID == "" {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 account ID is required")
	}
	if !hasToken || cfg.authToken == "" {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 API token is required")
	}
	if cfg.databaseID == "" {
		return dsnConfig{}, fmt.Errorf("Cloudflare D1 database ID is required")
	}
	for _, value := range []string{cfg.accountID, cfg.authToken, cfg.databaseID} {
		if strings.IndexFunc(value, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
			return dsnConfig{}, fmt.Errorf("Cloudflare D1 connection string contains control characters")
		}
	}
	return cfg, nil
}

type dbDriver struct{}

func (driverInstance *dbDriver) Open(name string) (driver.Conn, error) {
	connector, err := driverInstance.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

func (*dbDriver) OpenConnector(name string) (driver.Connector, error) {
	cfg, err := parseDSN(name)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg}, nil
}

type connector struct {
	cfg dsnConfig
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := cfd1.NewClient(c.cfg.accountID, c.cfg.authToken)
	handle, err := client.GetHandle(ctx, c.cfg.databaseID)
	if err != nil {
		return nil, sanitizedError("connect Cloudflare D1 database", err, c.cfg.authToken)
	}
	return &connection{client: client, databaseID: handle.UUID(), authToken: c.cfg.authToken}, nil
}

func (*connector) Driver() driver.Driver { return &dbDriver{} }

type rawQueryClient interface {
	RawQuery(context.Context, string, string, ...any) ([]cfd1.RawQueryResult, error)
}

type connection struct {
	client     rawQueryClient
	databaseID string
	authToken  string
}

func (c *connection) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *connection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return &statement{connection: c, query: query}, nil
}

func (c *connection) Close() error {
	c.client = nil
	c.authToken = ""
	return nil
}

func (*connection) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (*connection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (c *connection) Ping(ctx context.Context) error {
	if !c.IsValid() {
		return driver.ErrBadConn
	}
	_, err := c.query(ctx, "SELECT 1")
	return err
}

func (c *connection) ResetSession(ctx context.Context) error {
	if !c.IsValid() {
		return driver.ErrBadConn
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func (c *connection) IsValid() bool {
	return c != nil && c.client != nil && strings.TrimSpace(c.databaseID) != ""
}

func (c *connection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	params, err := namedValues(args)
	if err != nil {
		return nil, err
	}
	sets, err := c.query(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	var rowsAffected int64
	var lastInsertID int64
	for _, set := range sets {
		rowsAffected += int64(set.Meta.Changes)
		if set.Meta.LastRowID != 0 {
			lastInsertID = int64(set.Meta.LastRowID)
		}
	}
	return queryResult{lastInsertID: lastInsertID, rowsAffected: rowsAffected}, nil
}

func (c *connection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	params, err := namedValues(args)
	if err != nil {
		return nil, err
	}
	sets, err := c.query(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	return newRawRows(sets), nil
}

func (c *connection) query(ctx context.Context, query string, params ...any) ([]cfd1.RawQueryResult, error) {
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sets, err := c.client.RawQuery(ctx, c.databaseID, query, params...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, sanitizedError("Cloudflare D1 query failed", err, c.authToken)
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("Cloudflare D1 returned an incomplete query response")
	}
	for index, set := range sets {
		if !set.Success {
			return nil, fmt.Errorf("Cloudflare D1 statement %d failed", index+1)
		}
	}
	return sets, nil
}

type statement struct {
	connection *connection
	query      string
}

func (*statement) Close() error  { return nil }
func (*statement) NumInput() int { return -1 }

func (s *statement) Exec(values []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), valuesToNamed(values))
}

func (s *statement) Query(values []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), valuesToNamed(values))
}

func (s *statement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s == nil || s.connection == nil {
		return nil, driver.ErrBadConn
	}
	return s.connection.ExecContext(ctx, s.query, args)
}

func (s *statement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s == nil || s.connection == nil {
		return nil, driver.ErrBadConn
	}
	return s.connection.QueryContext(ctx, s.query, args)
}

type queryResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (result queryResult) LastInsertId() (int64, error) { return result.lastInsertID, nil }
func (result queryResult) RowsAffected() (int64, error) { return result.rowsAffected, nil }

type rawRows struct {
	sets     []cfd1.RawQueryResult
	setIndex int
	rowIndex int
	closed   bool
}

func newRawRows(sets []cfd1.RawQueryResult) *rawRows {
	setIndex := 0
	if len(sets) == 0 {
		setIndex = -1
	}
	return &rawRows{sets: sets, setIndex: setIndex}
}

func (rows *rawRows) Columns() []string {
	if rows == nil || rows.closed || rows.setIndex < 0 || rows.setIndex >= len(rows.sets) {
		return nil
	}
	return append([]string(nil), rows.sets[rows.setIndex].Results.Columns...)
}

func (rows *rawRows) Close() error {
	if rows != nil {
		rows.closed = true
		rows.sets = nil
	}
	return nil
}

func (rows *rawRows) Next(destination []driver.Value) error {
	if rows == nil || rows.closed || rows.setIndex < 0 || rows.setIndex >= len(rows.sets) {
		return io.EOF
	}
	current := rows.sets[rows.setIndex]
	if rows.rowIndex >= len(current.Results.Rows) {
		return io.EOF
	}
	row := current.Results.Rows[rows.rowIndex]
	if len(row) != len(current.Results.Columns) || len(destination) != len(current.Results.Columns) {
		return fmt.Errorf("Cloudflare D1 returned %d values for %d result columns", len(row), len(current.Results.Columns))
	}
	for index, value := range row {
		normalized, err := normalizeDriverValue(value)
		if err != nil {
			return fmt.Errorf("Cloudflare D1 column %q: %w", current.Results.Columns[index], err)
		}
		destination[index] = normalized
	}
	rows.rowIndex++
	return nil
}

func (rows *rawRows) HasNextResultSet() bool {
	return rows != nil && !rows.closed && rows.setIndex >= 0 && rows.setIndex+1 < len(rows.sets)
}

func (rows *rawRows) NextResultSet() error {
	if !rows.HasNextResultSet() {
		return io.EOF
	}
	rows.setIndex++
	rows.rowIndex = 0
	return nil
}

func normalizeDriverValue(value any) (driver.Value, error) {
	if driver.IsValue(value) {
		return value, nil
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return nil, fmt.Errorf("integer value exceeds database/sql range")
		}
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return nil, fmt.Errorf("integer value exceeds database/sql range")
		}
		return int64(typed), nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		floating, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid numeric value")
		}
		return floating, nil
	default:
		return nil, fmt.Errorf("unsupported result value type %T", value)
	}
}

func namedValues(values []driver.NamedValue) ([]any, error) {
	result := make([]any, len(values))
	for index, value := range values {
		if value.Name != "" {
			return nil, fmt.Errorf("Cloudflare D1 named parameters are not supported; use positional ? placeholders")
		}
		result[index] = value.Value
	}
	return result, nil
}

func valuesToNamed(values []driver.Value) []driver.NamedValue {
	result := make([]driver.NamedValue, len(values))
	for index, value := range values {
		result[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return result
}

func sanitizedError(prefix string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", prefix, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", prefix, context.DeadlineExceeded)
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return fmt.Errorf("%s: %s", prefix, message)
}

var (
	_ driver.Driver             = (*dbDriver)(nil)
	_ driver.DriverContext      = (*dbDriver)(nil)
	_ driver.Connector          = (*connector)(nil)
	_ driver.Conn               = (*connection)(nil)
	_ driver.ConnPrepareContext = (*connection)(nil)
	_ driver.ConnBeginTx        = (*connection)(nil)
	_ driver.ExecerContext      = (*connection)(nil)
	_ driver.QueryerContext     = (*connection)(nil)
	_ driver.Pinger             = (*connection)(nil)
	_ driver.SessionResetter    = (*connection)(nil)
	_ driver.Validator          = (*connection)(nil)
	_ driver.Stmt               = (*statement)(nil)
	_ driver.StmtExecContext    = (*statement)(nil)
	_ driver.StmtQueryContext   = (*statement)(nil)
	_ driver.Result             = queryResult{}
	_ driver.Rows               = (*rawRows)(nil)
	_ driver.RowsNextResultSet  = (*rawRows)(nil)
)
