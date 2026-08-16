package changeprofiler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	rowEncodingRaw  byte = 0
	rowEncodingZstd byte = 1
	minCompressRow       = 192
)

var (
	rowEncoderOnce sync.Once
	rowEncoder     *zstd.Encoder
	rowEncoderErr  error
	rowDecoderOnce sync.Once
	rowDecoder     *zstd.Decoder
	rowDecoderErr  error
)

type encodedCell struct {
	Name string `json:"n"`
	Kind string `json:"k"`
	Type string `json:"t,omitempty"`
	Data string `json:"v,omitempty"`
}

type encodedRow struct {
	Cells []encodedCell `json:"c"`
}

func encodeScannedRow(columnNames, databaseTypes []string, values []any) ([]byte, []byte, error) {
	row := encodedRow{Cells: make([]encodedCell, len(columnNames))}
	for index, name := range columnNames {
		databaseType := ""
		if index < len(databaseTypes) {
			databaseType = databaseTypes[index]
		}
		row.Cells[index] = normalizeCell(name, databaseType, values[index])
	}
	payload, err := json.Marshal(row)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(payload)
	return payload, hash[:], nil
}

// packRow keeps exact before/after values while substantially reducing the
// local footprint of wide or repetitive rows. The one-byte envelope is
// backwards compatible: legacy rows begin with JSON's '{' and are decoded as
// raw payloads by unpackRow.
func packRow(payload []byte) ([]byte, error) {
	if len(payload) < minCompressRow {
		return append([]byte{rowEncodingRaw}, payload...), nil
	}
	rowEncoderOnce.Do(func() {
		rowEncoder, rowEncoderErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(1),
			zstd.WithLowerEncoderMem(true),
		)
	})
	if rowEncoderErr != nil {
		return nil, rowEncoderErr
	}
	compressed := rowEncoder.EncodeAll(payload, nil)
	if len(compressed)+1 >= len(payload)+1 {
		return append([]byte{rowEncodingRaw}, payload...), nil
	}
	return append([]byte{rowEncodingZstd}, compressed...), nil
}

func unpackRow(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	switch payload[0] {
	case rowEncodingRaw:
		return payload[1:], nil
	case rowEncodingZstd:
		rowDecoderOnce.Do(func() {
			rowDecoder, rowDecoderErr = zstd.NewReader(nil,
				zstd.WithDecoderConcurrency(1),
				zstd.WithDecoderLowmem(true),
			)
		})
		if rowDecoderErr != nil {
			return nil, rowDecoderErr
		}
		return rowDecoder.DecodeAll(payload[1:], nil)
	default:
		// Anchors created before compact row envelopes stored JSON directly.
		return payload, nil
	}
}

func encodeKey(rowPayload []byte, keyColumns []string, kind KeyKind, occurrence int) ([]byte, []byte, error) {
	var row encodedRow
	if err := json.Unmarshal(rowPayload, &row); err != nil {
		return nil, nil, err
	}
	wanted := make(map[string]bool, len(keyColumns))
	for _, column := range keyColumns {
		wanted[column] = true
	}
	key := encodedRow{}
	if kind == KeyFullRow {
		key = row
	} else {
		for _, cell := range row.Cells {
			if wanted[cell.Name] {
				key.Cells = append(key.Cells, cell)
			}
		}
	}
	payload, err := json.Marshal(struct {
		Row        encodedRow `json:"r"`
		Occurrence int        `json:"o,omitempty"`
	}{Row: key, Occurrence: occurrence})
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(payload)
	return payload, hash[:], nil
}

func normalizeCell(name, databaseType string, value any) encodedCell {
	cell := encodedCell{Name: name, Type: databaseType}
	if value == nil {
		cell.Kind = "null"
		return cell
	}
	switch typed := value.(type) {
	case []byte:
		if databaseBytesAreText(databaseType) {
			cell.Kind, cell.Data = "text", string(typed)
		} else {
			cell.Kind, cell.Data = "bytes", base64.StdEncoding.EncodeToString(typed)
		}
	case string:
		cell.Kind, cell.Data = "text", typed
	case bool:
		cell.Kind, cell.Data = "bool", strconv.FormatBool(typed)
	case int64:
		cell.Kind, cell.Data = "int", strconv.FormatInt(typed, 10)
	case int:
		cell.Kind, cell.Data = "int", strconv.Itoa(typed)
	case float64:
		cell.Kind, cell.Data = "float", strconv.FormatUint(math.Float64bits(typed), 16)
	case time.Time:
		cell.Kind, cell.Data = "time", typed.Format(time.RFC3339Nano)
	case sql.RawBytes:
		cell.Kind, cell.Data = "bytes", base64.StdEncoding.EncodeToString([]byte(typed))
	default:
		cell.Kind, cell.Data = "text", fmt.Sprintf("%v", typed)
	}
	return cell
}

func databaseBytesAreText(databaseType string) bool {
	switch strings.ToUpper(strings.TrimSpace(databaseType)) {
	case "BYTEA", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BIT", "GEOMETRY", "VECTOR":
		return false
	default:
		return true
	}
}

func decodeRow(payload []byte) (map[string]Value, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	var err error
	payload, err = unpackRow(payload)
	if err != nil {
		return nil, err
	}
	var row encodedRow
	if err := json.Unmarshal(payload, &row); err != nil {
		return nil, err
	}
	result := make(map[string]Value, len(row.Cells))
	for _, cell := range row.Cells {
		value := Value{Kind: cell.Kind, Type: cell.Type, Text: cell.Data, Null: cell.Kind == "null"}
		if cell.Kind == "bytes" {
			if raw, err := base64.StdEncoding.DecodeString(cell.Data); err == nil {
				value.Text = fmt.Sprintf("0x%s", hex.EncodeToString(raw))
			}
		} else if cell.Kind == "float" {
			if bits, err := strconv.ParseUint(cell.Data, 16, 64); err == nil {
				value.Text = strconv.FormatFloat(math.Float64frombits(bits), 'g', -1, 64)
			}
		} else if cell.Kind == "null" {
			value.Text = "NULL"
		}
		result[cell.Name] = value
	}
	return result, nil
}

func changedColumns(beforePayload, afterPayload []byte) ([]string, error) {
	var before, after encodedRow
	if err := json.Unmarshal(beforePayload, &before); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(afterPayload, &after); err != nil {
		return nil, err
	}
	left := make(map[string]encodedCell, len(before.Cells))
	for _, cell := range before.Cells {
		left[cell.Name] = cell
	}
	changed := make([]string, 0)
	seen := make(map[string]bool, len(after.Cells))
	for _, cell := range after.Cells {
		seen[cell.Name] = true
		if old, ok := left[cell.Name]; !ok || old != cell {
			changed = append(changed, cell.Name)
		}
	}
	for _, cell := range before.Cells {
		if !seen[cell.Name] {
			changed = append(changed, cell.Name)
		}
	}
	return changed, nil
}
