package schema

import (
	"sort"
	"strings"
	"unicode"
)

// Settings search: a pure-Go index over a built NavSchema plus a ranked, accent- and
// case-insensitive matcher. All strings in the schema are already translated, so searching
// works in the user's language without any extra wiring. The UI adapter only renders results.

// maxSearchResults caps the number of results Search returns.
const maxSearchResults = 50

// SearchEntry is one searchable thing: a preferences page or a single setting row.
type SearchEntry struct {
	PageID       string   // Page to open (a child ID for a museum page)
	PageTrail    []string // Page titles from the top-level page down, e.g. ["Museums", "The Met"]
	SectionTitle string   // Section (or sub-heading) the row sits under; empty for pages
	SettingName  string   // Name/ID of the row (empty for pages and unnamed rows)
	Label        string   // Row label; for a page entry, the page title
	Help         string   // Help text; for a page entry, its description
	Options      []string // Option labels, preset labels, card titles and descriptions, button text
	IsPage       bool     // True for a page entry
}

// SearchResult is a matched entry. Lower Score is better.
type SearchResult struct {
	SearchEntry
	Score int
}

// BuildSearchIndex flattens the navigation schema into search entries in navigation order:
// each page's own entry first, then its settings, then its child pages.
func BuildSearchIndex(nav *NavSchema) []SearchEntry {
	if nav == nil {
		return nil
	}
	var out []SearchEntry
	for _, g := range nav.Groups {
		for i := range g.Items {
			indexNavItem(&out, g.Items[i], nil)
		}
	}
	return out
}

func indexNavItem(out *[]SearchEntry, item NavItem, parents []string) {
	title := item.Title
	if title == "" && item.TitleFunc != nil {
		title = item.TitleFunc()
	}
	trail := append(append([]string{}, parents...), title)

	desc := item.Description
	if desc == "" && item.Panel != nil {
		desc = item.Panel.Description
	}
	if title != "" {
		*out = append(*out, SearchEntry{PageID: item.ID, PageTrail: trail, Label: title, Help: desc, IsPage: true})
	}

	if item.Panel != nil {
		for _, sec := range item.Panel.Sections {
			heading := sec.Title
			for _, it := range sec.Items {
				if li, ok := it.(LabelItem); ok && li.IsTitle {
					if li.Text != "" {
						heading = li.Text // a sub-heading starts a new group inside the section
					}
					continue
				}
				indexItem(out, it, item.ID, trail, heading)
			}
		}
	}
	for ci := range item.Children {
		indexNavItem(out, item.Children[ci], trail)
	}
}

// indexItem adds the entry (or entries, for a horizontal row) for one schema item.
func indexItem(out *[]SearchEntry, item ItemSchema, pageID string, trail []string, heading string) {
	e := SearchEntry{PageID: pageID, PageTrail: trail, SectionTitle: heading}
	switch v := item.(type) {
	case BoolItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = []string{v.ActionText}
	case TextItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		for _, p := range v.Presets {
			e.Options = append(e.Options, p.Label)
		}
	case SelectItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = append(e.Options, v.Options...)
	case SliderItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
	case ChoiceCardsItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		for _, c := range v.Cards {
			e.Options = append(e.Options, c.Title, c.Description)
		}
	case AsyncButtonItem:
		e.SettingName, e.Label = v.Name, v.ButtonText
	case ConfirmButtonItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = []string{v.ButtonText, v.ConfirmTitle, v.ConfirmMessage}
	case ButtonItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = []string{v.ButtonText}
	case HyperlinkItem:
		e.SettingName, e.Label = v.ID, v.Text
	case OAuthPickerItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
	case *OAuthPickerItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
	case FolderPickerItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = []string{v.ButtonText}
	case AppPickerItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
		e.Options = []string{v.ButtonText}
	case SecretItem:
		e.SettingName, e.Label, e.Help = v.Name, v.Label, v.Help
	case HorizontalRowItem:
		for _, child := range v.Items {
			indexItem(out, child, pageID, trail, heading)
		}
		return
	case *HorizontalRowItem:
		for _, child := range v.Items {
			indexItem(out, child, pageID, trail, heading)
		}
		return
	default:
		return // stats, previews, query lists and plain labels carry nothing to find
	}

	e.Options = nonEmpty(e.Options)
	if e.Label == "" {
		if e.Help == "" && len(e.Options) == 0 {
			return
		}
		// A label-less row (e.g. choice cards) is found and shown under its section's name,
		// or under its page's when the section has none.
		e.Label, e.SectionTitle = heading, ""
		if e.Label == "" && len(trail) > 0 {
			e.Label = trail[len(trail)-1]
		}
	}
	*out = append(*out, e)
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Match tiers, best first. A word's tier is the best field it is found in.
const (
	tierLabelStart = iota // the label begins with the word
	tierLabelWord         // a word of the label begins with the word
	tierLabelAny          // the label contains the word
	tierPage              // a page title on the trail contains the word
	tierSection           // the section title contains the word
	tierHelp              // the help text contains the word
	tierOption            // an option, card or button text contains the word
	tierNoMatch    = 99
)

// Search returns the entries matching every word of query (case- and accent-insensitive),
// best matches first: label starts with the word, label contains it, page title, section title,
// help text, then option text. Equal matches keep navigation order, and a setting appears once.
// An empty query returns nil. At most 50 results are returned.
func Search(entries []SearchEntry, query string) []SearchResult {
	words := strings.Fields(foldSearchText(query))
	if len(words) == 0 {
		return nil
	}

	type scored struct {
		res      SearchResult
		worst    int
		sum, idx int
	}
	var hits []scored
	seen := map[string]int{} // dedupe key -> index in hits
	for idx, e := range entries {
		f := foldEntry(e)
		worst, sum := 0, 0
		matched := true
		for _, w := range words {
			t := f.wordTier(w)
			if t == tierNoMatch {
				matched = false
				break
			}
			if t > worst {
				worst = t
			}
			sum += t
		}
		if !matched {
			continue
		}
		h := scored{res: SearchResult{SearchEntry: e, Score: worst*100 + sum}, worst: worst, sum: sum, idx: idx}
		key := dedupeKey(e)
		if prev, dup := seen[key]; dup {
			if h.res.Score < hits[prev].res.Score {
				h.idx = hits[prev].idx
				hits[prev] = h
			}
			continue
		}
		seen[key] = len(hits)
		hits = append(hits, h)
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].res.Score != hits[j].res.Score {
			return hits[i].res.Score < hits[j].res.Score
		}
		return hits[i].idx < hits[j].idx
	})
	if len(hits) == 0 {
		return nil
	}
	if len(hits) > maxSearchResults {
		hits = hits[:maxSearchResults]
	}
	out := make([]SearchResult, len(hits))
	for i, h := range hits {
		out[i] = h.res
	}
	return out
}

func dedupeKey(e SearchEntry) string {
	switch {
	case e.IsPage:
		return "page\x00" + e.PageID
	case e.SettingName != "":
		return "set\x00" + e.PageID + "\x00" + e.SettingName
	}
	return "row\x00" + e.PageID + "\x00" + e.Label + "\x00" + e.SectionTitle
}

// foldedEntry is an entry's searchable text, normalized once per search.
type foldedEntry struct {
	label      string
	labelWords []string
	trail      string
	section    string
	help       string
	options    []string
}

func foldEntry(e SearchEntry) foldedEntry {
	f := foldedEntry{
		label:   foldSearchText(e.Label),
		trail:   foldSearchText(strings.Join(e.PageTrail, " ")),
		section: foldSearchText(e.SectionTitle),
		help:    foldSearchText(e.Help),
	}
	f.labelWords = splitSearchWords(f.label)
	for _, o := range e.Options {
		f.options = append(f.options, foldSearchText(o))
	}
	return f
}

// wordTier returns the best tier at which the (already folded) word occurs, or tierNoMatch.
func (f foldedEntry) wordTier(w string) int {
	switch {
	case strings.HasPrefix(f.label, w):
		return tierLabelStart
	case labelWordHasPrefix(f.labelWords, w):
		return tierLabelWord
	case strings.Contains(f.label, w):
		return tierLabelAny
	case strings.Contains(f.trail, w):
		return tierPage
	case strings.Contains(f.section, w):
		return tierSection
	case strings.Contains(f.help, w):
		return tierHelp
	}
	for _, o := range f.options {
		if strings.Contains(o, w) {
			return tierOption
		}
	}
	return tierNoMatch
}

func labelWordHasPrefix(words []string, w string) bool {
	for _, lw := range words {
		if strings.HasPrefix(lw, w) {
			return true
		}
	}
	return false
}

func splitSearchWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// foldSearchText lower-cases s and strips accents so "Fréquence" matches "frequence". It handles
// precomposed Latin letters and the combining marks of decomposed text, plus a few letters that
// do not decompose (ß, æ, œ, ø, ł, đ, ð, þ) and Cyrillic й/ё.
func foldSearchText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue // combining accent of decomposed text
		}
		r = unicode.ToLower(r)
		if rep, ok := foldTable[r]; ok {
			b.WriteString(rep)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var foldTable = buildFoldTable()

func buildFoldTable() map[rune]string {
	m := map[rune]string{
		'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th",
		'ı': "i", 'й': "и", 'ё': "е", 'ґ': "г", 'ї': "і", 'є': "е",
	}
	groups := map[string]string{
		"a": "àáâãäåāăąǎ",
		"c": "çćĉċč",
		"d": "ď",
		"e": "èéêëēĕėęěẽ",
		"g": "ĝğġģ",
		"h": "ĥ",
		"i": "ìíîïĩīĭįǐ",
		"j": "ĵ",
		"k": "ķ",
		"l": "ĺļľ",
		"n": "ñńņňŉ",
		"o": "òóôõöōŏőǒ",
		"r": "ŕŗř",
		"s": "śŝşšș",
		"t": "ţťț",
		"u": "ùúûüũūŭůűųǔ",
		"w": "ŵ",
		"y": "ýÿŷ",
		"z": "źżž",
	}
	for base, accented := range groups {
		for _, r := range accented {
			m[r] = base
		}
	}
	return m
}
