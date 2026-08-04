package d1sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/peterheb/cfd1"
)

func TestParseDSNRoundTripsEscapedCredentials(t *testing.T) {
	want := dsnConfig{accountID: "account@example", authToken: "token:/with?reserved#characters", databaseID: "database-uuid"}
	raw := (&url.URL{Scheme: "d1", User: url.UserPassword(want.accountID, want.authToken), Host: want.databaseID}).String()
	got, err := parseDSN(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("parseDSN() = %#v, want %#v", got, want)
	}
}

func TestParseDSNRejectsIncompleteOrAmbiguousInputsWithoutEchoingSecrets(t *testing.T) {
	secret := "private-token"
	for _, raw := range []string{
		"",
		"https://account:" + secret + "@database",
		"d1://account@database",
		"d1://account:" + secret + "@database/path",
		"d1://account:" + secret + "@database?mode=unsafe",
	} {
		if _, err := parseDSN(raw); err == nil {
			t.Errorf("parseDSN(%q) succeeded", raw)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("parseDSN(%q) leaked its token: %v", raw, err)
		}
	}
}

func TestRawRowsHandlesEmptyResultAndPreservesColumnOrder(t *testing.T) {
	set := rawSet([]string{"zeta", "alpha"})
	rows := newRawRows([]cfd1.RawQueryResult{set})
	if got := rows.Columns(); !reflect.DeepEqual(got, []string{"zeta", "alpha"}) {
		t.Fatalf("Columns() = %#v", got)
	}
	if err := rows.Next(make([]driver.Value, 2)); !errors.Is(err, io.EOF) {
		t.Fatalf("empty Next() error = %v, want EOF", err)
	}
}

func TestRawRowsReturnsValuesInAPIMetadataOrder(t *testing.T) {
	set := rawSet([]string{"name", "id"}, []any{"Ada", float64(7)})
	rows := newRawRows([]cfd1.RawQueryResult{set})
	destination := make([]driver.Value, 2)
	if err := rows.Next(destination); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(destination, []driver.Value{"Ada", float64(7)}) {
		t.Fatalf("row = %#v", destination)
	}
}

func TestRawRowsSupportsEmptyAndPopulatedResultSets(t *testing.T) {
	first := rawSet([]string{"empty"})
	second := rawSet([]string{"value"}, []any{"present"})
	rows := newRawRows([]cfd1.RawQueryResult{first, second})
	if err := rows.Next(make([]driver.Value, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("first set Next() = %v", err)
	}
	if !rows.HasNextResultSet() {
		t.Fatal("second result set was not exposed")
	}
	if err := rows.NextResultSet(); err != nil {
		t.Fatal(err)
	}
	destination := make([]driver.Value, 1)
	if err := rows.Next(destination); err != nil || destination[0] != "present" {
		t.Fatalf("second set row = %#v, %v", destination, err)
	}
}

func TestNormalizeDriverValueBoundsIntegers(t *testing.T) {
	if got, err := normalizeDriverValue(int(42)); err != nil || got != int64(42) {
		t.Fatalf("normalize int = %#v, %v", got, err)
	}
	if _, err := normalizeDriverValue(uint64(math.MaxUint64)); err == nil {
		t.Fatal("out-of-range uint64 succeeded")
	}
	if _, err := normalizeDriverValue(struct{}{}); err == nil {
		t.Fatal("unsupported result value succeeded")
	}
}

func TestConnectionUsesRawQueryForEmptyOrderedResults(t *testing.T) {
	client := &fakeRawClient{sets: []cfd1.RawQueryResult{rawSet([]string{"first", "second"})}}
	connection := &connection{client: client, databaseID: "database-uuid", authToken: "private"}
	result, err := connection.QueryContext(context.Background(), "SELECT first, second FROM empty_table", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.databaseID != "database-uuid" || client.query == "" {
		t.Fatalf("raw client call = database %q, query %q", client.databaseID, client.query)
	}
	if got := result.Columns(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("ordered columns = %#v", got)
	}
	if err := result.Next(make([]driver.Value, 2)); !errors.Is(err, io.EOF) {
		t.Fatalf("empty raw result = %v", err)
	}
}

func TestDatabaseSQLCanConsumeSuccessfulZeroRowQuery(t *testing.T) {
	client := &fakeRawClient{sets: []cfd1.RawQueryResult{rawSet([]string{"first", "second"})}}
	conn := &connection{client: client, databaseID: "database-uuid", authToken: "private"}
	database := sql.OpenDB(fixedConnector{connection: conn})
	defer database.Close()

	rows, err := database.QueryContext(context.Background(), "SELECT first, second FROM empty_table")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(columns, []string{"first", "second"}) {
		t.Fatalf("database/sql columns = %#v", columns)
	}
	if rows.Next() {
		t.Fatal("zero-row query unexpectedly returned a row")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("zero-row query ended with error: %v", err)
	}
}

func TestConnectionRedactsQueryErrors(t *testing.T) {
	client := &fakeRawClient{err: errors.New("request rejected for private-token")}
	connection := &connection{client: client, databaseID: "database-uuid", authToken: "private-token"}
	_, err := connection.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil || strings.Contains(err.Error(), "private-token") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("redacted query error = %v", err)
	}
}

func TestConnectionRejectsIncompleteOrSoftFailedResponses(t *testing.T) {
	tests := []struct {
		name string
		sets []cfd1.RawQueryResult
		want string
	}{
		{name: "no result set", want: "incomplete query response"},
		{name: "soft failure", sets: []cfd1.RawQueryResult{rawSet([]string{"ok"}), rawSet([]string{"failed"})}, want: "statement 2 failed"},
	}
	tests[1].sets[1].Success = false
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &connection{client: &fakeRawClient{sets: test.sets}, databaseID: "database-uuid", authToken: "private-token"}
			if _, err := connection.QueryContext(context.Background(), "SELECT 1", nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("QueryContext() error = %v, want %q", err, test.want)
			}
			if _, err := connection.ExecContext(context.Background(), "SELECT 1", nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExecContext() error = %v, want %q", err, test.want)
			}
			if err := connection.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ping() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPrepareContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := &connection{client: &fakeRawClient{}, databaseID: "database-uuid"}
	if _, err := connection.PrepareContext(ctx, "SELECT 1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareContext() error = %v, want context cancellation", err)
	}
}

type fakeRawClient struct {
	sets       []cfd1.RawQueryResult
	err        error
	databaseID string
	query      string
	params     []any
}

type fixedConnector struct {
	connection driver.Conn
}

func (connector fixedConnector) Connect(context.Context) (driver.Conn, error) {
	return connector.connection, nil
}

func (fixedConnector) Driver() driver.Driver { return &dbDriver{} }

func (client *fakeRawClient) RawQuery(_ context.Context, databaseID, query string, params ...any) ([]cfd1.RawQueryResult, error) {
	client.databaseID = databaseID
	client.query = query
	client.params = append([]any(nil), params...)
	return client.sets, client.err
}

func rawSet(columns []string, rows ...[]any) cfd1.RawQueryResult {
	var result cfd1.RawQueryResult
	result.Results.Columns = append([]string(nil), columns...)
	result.Results.Rows = append([][]any(nil), rows...)
	result.Success = true
	return result
}
