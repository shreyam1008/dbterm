package ui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFetchLatestReleaseUsesOfficialLatestEndpoint(t *testing.T) {
	client := &http.Client{Transport: updateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.String(); got != "https://api.github.com/repos/shreyam1008/dbterm/releases/latest" {
			t.Fatalf("request URL = %q", got)
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("Accept header = %q", request.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
  "tag_name": "v0.9.1",
  "name": "Chenab Patch",
  "body": "Data safety",
  "html_url": "https://github.com/shreyam1008/dbterm/releases/tag/v0.9.1"
}`)),
			Request: request,
		}, nil
	})}

	release, err := fetchLatestRelease(context.Background(), client, "shreyam1008/dbterm")
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if release.TagName != "v0.9.1" || release.Name != "Chenab Patch" {
		t.Fatalf("release = %#v", release)
	}
}

func TestUpdateAvailableUsesNumericSemver(t *testing.T) {
	for _, test := range []struct {
		current string
		latest  string
		want    bool
	}{
		{current: "0.9.0", latest: "v0.9.1", want: true},
		{current: "0.10.0", latest: "0.9.9", want: false},
		{current: "1.0.0", latest: "v1.0.0", want: false},
		{current: "dev", latest: "0.9.1", want: true},
	} {
		if got := updateAvailable(test.current, test.latest); got != test.want {
			t.Fatalf("updateAvailable(%q, %q) = %t, want %t", test.current, test.latest, got, test.want)
		}
	}
}

func TestNormalizeBuildInfoRejectsUnsafeRepository(t *testing.T) {
	got := normalizeBuildInfo(BuildInfo{Version: "v0.9.1", Repository: "../bad"})
	if got.Version != "0.9.1" || got.Repository != defaultUpdateRepository {
		t.Fatalf("normalized build info = %#v", got)
	}
}

func TestCompactReleaseNotesBoundsRunes(t *testing.T) {
	if got := compactReleaseNotes(" one\n two   three ", 9); got != "one two…" {
		t.Fatalf("compactReleaseNotes() = %q", got)
	}
}
