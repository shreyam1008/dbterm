package folderpicker

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseSelectionPreservesValidWhitespace(t *testing.T) {
	selection, err := parseSelection([]byte(" /srv/backup files \r\n"))
	if err != nil {
		t.Fatalf("parse selection: %v", err)
	}
	if selection != " /srv/backup files " {
		t.Fatalf("selection = %q", selection)
	}
}

func TestParseSelectionRejectsEmptyAndNUL(t *testing.T) {
	if _, err := parseSelection([]byte("\r\n")); !errors.Is(err, ErrCancelled) {
		t.Fatalf("empty selection error = %v, want ErrCancelled", err)
	}
	if _, err := parseSelection([]byte("bad\x00path\n")); err == nil {
		t.Fatal("NUL selection succeeded")
	}
}

func TestEnvironmentWithValueReplacesCaseInsensitively(t *testing.T) {
	got := environmentWithValue([]string{"A=1", "dbterm_folder_picker_start=old", "B=2"}, "DBTERM_FOLDER_PICKER_START", "new")
	want := []string{"A=1", "B=2", "DBTERM_FOLDER_PICKER_START=new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}
