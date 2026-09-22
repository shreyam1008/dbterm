// Package uniseg provides the small compatibility surface used by tcell and
// tview for terminal text layout.
//
// The upstream uniseg API is kept here because tcell and tview import it
// directly. Grapheme boundaries come from Unicode's extended grapheme rules,
// including Indic conjuncts, while widths are calculated for terminal cells.
package uniseg

import (
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/SCKelemen/unicode/v6/uax11"
	"github.com/SCKelemen/unicode/v6/uax14"
	"github.com/SCKelemen/unicode/v6/uax29"
	"github.com/SCKelemen/unicode/v6/uts51"
)

// Boundary masks returned by Step and StepString.
const (
	MaskLine     = 3
	MaskWord     = 4
	MaskSentence = 8
	ShiftWidth   = 4
)

// Line-break values returned in the MaskLine bits.
const (
	LineDontBreak = iota
	LineCanBreak
	LineMustBreak
)

// EastAsianAmbiguousWidth controls the width assigned to East Asian Ambiguous
// characters. tcell changes this value when RUNEWIDTH_EASTASIAN is set.
var EastAsianAmbiguousWidth = 1

// firstClusterEnd returns the byte end of the first extended grapheme cluster.
// Invalid UTF-8 is handled one decoded rune at a time so malformed database
// text cannot make the renderer stall or slice at the wrong byte boundary.
func firstClusterEnd(text string) int {
	if text == "" {
		return 0
	}
	if !utf8.ValidString(text) {
		_, size := utf8.DecodeRuneInString(text)
		if size == 0 {
			return len(text)
		}
		return size
	}

	breaks := uax29.FindGraphemeBreaks(text)
	if len(breaks) >= 2 && breaks[1] > 0 && breaks[1] <= len(text) {
		return breaks[1]
	}
	_, size := utf8.DecodeRuneInString(text)
	return size
}

func isZeroWidth(r rune) bool {
	return unicode.IsControl(r) ||
		unicode.Is(unicode.Mn, r) ||
		unicode.Is(unicode.Me, r) ||
		unicode.Is(unicode.Mc, r) ||
		unicode.Is(unicode.Cf, r)
}

func runeWidth(r rune) int {
	if isZeroWidth(r) {
		return 0
	}

	switch r {
	case 0x2e3a: // TWO-EM DASH
		return 3
	case 0x2e3b: // THREE-EM DASH
		return 4
	}

	switch uax11.LookupWidth(r) {
	case uax11.Fullwidth, uax11.Wide:
		return 2
	case uax11.Ambiguous:
		return EastAsianAmbiguousWidth
	default:
		return 1
	}
}

func clusterWidth(cluster string) int {
	runes := []rune(cluster)
	if emojiWidth := uts51.EmojiSequenceWidth(runes); emojiWidth >= 0 {
		return emojiWidth
	}

	width := 0
	for _, r := range runes {
		if w := runeWidth(r); w > width {
			width = w
		}
	}
	return width
}

func containsBreak(breaks []int, position int) bool {
	index := sort.SearchInts(breaks, position)
	return index < len(breaks) && breaks[index] == position
}

func boundaryInfo(text, cluster string, end int) int {
	boundaries := clusterWidth(cluster) << ShiftWidth

	if containsBreak(uax29.FindWordBreaks(text), end) {
		boundaries |= MaskWord
	}
	if containsBreak(uax29.FindSentenceBreaks(text), end) {
		boundaries |= MaskSentence
	}

	// UAX #14 requires the final cluster to report a mandatory break. tview
	// removes that flag for a final cluster without a hard line break.
	lineBreak := LineDontBreak
	if end == len(text) {
		lineBreak = LineMustBreak
	} else if containsBreak(uax14.FindLineBreakOpportunities(text, uax14.HyphensManual), end) {
		lineBreak = LineCanBreak
	}
	if HasTrailingLineBreakInString(cluster) {
		lineBreak = LineMustBreak
	}
	boundaries |= lineBreak

	return boundaries
}

// FirstGraphemeCluster returns the first user-perceived character in b.
func FirstGraphemeCluster(b []byte, state int) (cluster, rest []byte, width, newState int) {
	clusterString, restString, width, newState := FirstGraphemeClusterInString(string(b), state)
	if clusterString == "" {
		return nil, nil, width, newState
	}
	return []byte(clusterString), []byte(restString), width, newState
}

// FirstGraphemeClusterInString returns the first user-perceived character in
// str and its terminal-cell width.
func FirstGraphemeClusterInString(str string, state int) (cluster, rest string, width, newState int) {
	if str == "" {
		return "", "", 0, state
	}

	end := firstClusterEnd(str)
	cluster, rest = str[:end], str[end:]
	return cluster, rest, clusterWidth(cluster), 0
}

// Step returns the first grapheme cluster and boundary metadata in b.
func Step(b []byte, state int) (cluster, rest []byte, boundaries, newState int) {
	clusterString, restString, boundaries, newState := StepString(string(b), state)
	if clusterString == "" {
		return nil, nil, boundaries, newState
	}
	return []byte(clusterString), []byte(restString), boundaries, newState
}

// StepString returns the first grapheme cluster and boundary metadata in str.
func StepString(str string, state int) (cluster, rest string, boundaries, newState int) {
	if str == "" {
		return "", "", 0, state
	}

	end := firstClusterEnd(str)
	cluster, rest = str[:end], str[end:]
	return cluster, rest, boundaryInfo(str, cluster, end), 0
}

// StringWidth returns the number of terminal cells occupied by s.
func StringWidth(s string) (width int) {
	for len(s) > 0 {
		var clusterWidthValue int
		_, s, clusterWidthValue, _ = FirstGraphemeClusterInString(s, -1)
		width += clusterWidthValue
	}
	return width
}

// GraphemeClusterCount returns the number of user-perceived characters in s.
func GraphemeClusterCount(s string) (count int) {
	for len(s) > 0 {
		_, s, _, _ = FirstGraphemeClusterInString(s, -1)
		count++
	}
	return count
}

// ReverseString reverses s by grapheme cluster rather than by code point.
func ReverseString(s string) string {
	clusters := graphemeClusters(s)
	for left, right := 0, len(clusters)-1; left < right; left, right = left+1, right-1 {
		clusters[left], clusters[right] = clusters[right], clusters[left]
	}
	return joinClusters(clusters)
}

// Graphemes iterates over the extended grapheme clusters in a string.
type Graphemes struct {
	original   string
	remaining  string
	cluster    string
	offset     int
	boundaries int
	state      int
}

// NewGraphemes returns a new grapheme-cluster iterator.
func NewGraphemes(str string) *Graphemes {
	return &Graphemes{original: str, remaining: str, state: -1}
}

// Next advances the iterator and reports whether a cluster was found.
func (g *Graphemes) Next() bool {
	if g.remaining == "" {
		g.state = -2
		g.cluster = ""
		return false
	}
	g.offset += len(g.cluster)
	g.cluster, g.remaining, g.boundaries, g.state = StepString(g.remaining, g.state)
	return true
}

// Runes returns the runes in the current cluster.
func (g *Graphemes) Runes() []rune {
	if g.state < 0 {
		return nil
	}
	return []rune(g.cluster)
}

// Str returns the current cluster.
func (g *Graphemes) Str() string {
	return g.cluster
}

// Bytes returns the current cluster as UTF-8 bytes.
func (g *Graphemes) Bytes() []byte {
	if g.state < 0 {
		return nil
	}
	return []byte(g.cluster)
}

// Positions returns the current cluster's byte range in the original string.
func (g *Graphemes) Positions() (from, to int) {
	if g.state == -1 {
		return 0, 0
	}
	if g.state == -2 {
		return 1, 1
	}
	return g.offset, g.offset + len(g.cluster)
}

// IsWordBoundary reports whether a word ends after the current cluster.
func (g *Graphemes) IsWordBoundary() bool {
	if g.state < 0 {
		return true
	}
	return g.boundaries&MaskWord != 0
}

// IsSentenceBoundary reports whether a sentence ends after the current cluster.
func (g *Graphemes) IsSentenceBoundary() bool {
	if g.state < 0 {
		return true
	}
	return g.boundaries&MaskSentence != 0
}

// LineBreak reports whether a line can or must break after the current cluster.
func (g *Graphemes) LineBreak() int {
	if g.state == -1 {
		return LineDontBreak
	}
	if g.state == -2 {
		return LineMustBreak
	}
	return g.boundaries & MaskLine
}

// Width returns the terminal-cell width of the current cluster.
func (g *Graphemes) Width() int {
	if g.state < 0 {
		return 0
	}
	return g.boundaries >> ShiftWidth
}

// Reset returns the iterator to its initial state.
func (g *Graphemes) Reset() {
	g.remaining = g.original
	g.cluster = ""
	g.offset = 0
	g.boundaries = 0
	g.state = -1
}

func graphemeClusters(s string) []string {
	if s == "" {
		return nil
	}

	clusters := make([]string, 0, GraphemeClusterCount(s))
	for len(s) > 0 {
		cluster, rest, _, _ := FirstGraphemeClusterInString(s, -1)
		clusters = append(clusters, cluster)
		s = rest
	}
	return clusters
}

func joinClusters(clusters []string) string {
	var total int
	for _, cluster := range clusters {
		total += len(cluster)
	}
	result := make([]byte, 0, total)
	for _, cluster := range clusters {
		result = append(result, cluster...)
	}
	return string(result)
}

func hasTrailingHardBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

// HasTrailingLineBreak reports whether b ends with a hard line-break rune.
func HasTrailingLineBreak(b []byte) bool {
	return HasTrailingLineBreakInString(string(b))
}

// HasTrailingLineBreakInString reports whether str ends with a hard line-break
// rune.
func HasTrailingLineBreakInString(str string) bool {
	if str == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(str)
	return hasTrailingHardBreak(r)
}
