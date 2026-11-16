package intercept

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Watch finds a phrase or pattern in messages, including the model's reply
// text as it streams: a match split across pieces is seen whole. Only the
// tail that could still become a match is held back from the client;
// everything before it goes on. Messages to the model are whole, so there a
// watch runs directly.
type Watch struct {
	// Phrase to find, ignoring case; or Pattern, with MaxLen the longest
	// match it can make (the text held back while one could be completing).
	Phrase  string
	Pattern *regexp.Regexp
	MaxLen  int
	// In is the direction it watches (0: both).
	In Direction
	// On answers each match: Replace (the match alone), Reply (the reply
	// ends there with the text), or Accept.
	On func(ctx context.Context, w *Match) Answer
}

func (Watch) item() {}

// Match is one match of a watch.
type Match struct {
	// Text is what matched.
	Text    string
	Message *Message
	Call    *Call
	Watch   *Watch
}

// Replace puts text in place of the match alone.
func (w *Match) Replace(text string) Answer { return Replace(text) }

// Reply ends the reply at the match with text (on a request: answers the
// call with it).
func (w *Match) Reply(text string) Answer { return Reply(text) }

// Accept leaves the match as it is.
func (w *Match) Accept() Answer { return Accept() }

type watcher struct {
	w      *Watch
	re     *regexp.Regexp
	phrase string // lower case, for a phrase
	maxLen int
}

func newWatcher(w *Watch) watcher {
	if w.Phrase != "" {
		return watcher{w: w, re: regexp.MustCompile("(?i)" + regexp.QuoteMeta(w.Phrase)), phrase: strings.ToLower(w.Phrase), maxLen: len(w.Phrase)}
	}
	n := w.MaxLen
	if n <= 0 {
		n = DefaultMaxLen
	}
	return watcher{w: w, re: w.Pattern, maxLen: n}
}

type hit struct {
	start, end int
	w          *watcher
}

// scanned is what the watches made of a text: out (the text with their
// replacements, or up to a Reply), keep (bytes at the end of the text held
// back), reply (a Reply's answer: out is then the text before the match).
type scanned struct {
	out     string
	changed bool
	keep    int
	reply   *answer
	notes   [][2]string
	err     error
}

// scan runs the watches over text, of which the first skip bytes are
// context only (text already released): a match must end after them. Unless
// final, it holds back the tail that could still become a match, and leaves
// alone a pattern's match that could still grow.
func scan(ctx context.Context, ws []watcher, m *Message, text string, skip int, final bool) scanned {
	var hits []hit
	keep := 0
	for i := range ws {
		w := &ws[i]
		bound := len(text) // a match must end by here to count
		if !final && w.phrase == "" {
			bound = max(skip, len(text)-(w.maxLen-1))
		}
		for _, loc := range w.re.FindAllStringIndex(text, -1) {
			if loc[1] <= skip || loc[1] == loc[0] {
				continue
			}
			if loc[1] > bound {
				keep = max(keep, len(text)-max(skip, min(loc[0], bound)))
				continue
			}
			hits = append(hits, hit{loc[0], loc[1], w})
		}
		if !final {
			if w.phrase != "" {
				keep = max(keep, prefixTail(text[skip:], w.phrase))
			} else {
				keep = max(keep, len(text)-bound)
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
	var sb strings.Builder
	s := scanned{}
	at, last := skip, 0
	for _, h := range hits {
		if h.start < last {
			continue // overlaps an earlier match
		}
		a := get(h.w.w.On(ctx, &Match{Text: text[h.start:h.end], Message: m, Call: m.Call, Watch: h.w.w}))
		s.notes = append(s.notes, a.notes...)
		switch a.verb {
		case vReplace:
			if h.start > at {
				sb.WriteString(text[at:h.start])
			}
			sb.WriteString(a.text)
			at, last, s.changed = h.end, h.end, true
		case vReply:
			if h.start > at {
				sb.WriteString(text[at:h.start])
			}
			s.out, s.reply = sb.String(), a
			return s
		case vFail:
			s.err = a.err
			return s
		}
	}
	sb.WriteString(text[at:])
	s.out = sb.String()
	// Hold back nothing already replaced; cut on a character.
	keep = min(keep, len(text)-at)
	for keep > 0 && keep < len(text)-skip && !utf8.RuneStart(text[len(text)-keep]) {
		keep++
	}
	s.keep = keep
	return s
}

// releasedTail is the end of text already released that a match could start in:
// the longest watch's length less one, cut on a character.
func releasedTail(ws []watcher, released string) string {
	n := 0
	for _, w := range ws {
		n = max(n, w.maxLen-1)
	}
	if n >= len(released) {
		return released
	}
	i := len(released) - n
	for i < len(released) && !utf8.RuneStart(released[i]) {
		i++
	}
	return released[i:]
}

// prefixTail is the length of the longest tail of text that is a proper
// start of phrase (lower case), ignoring case.
func prefixTail(text, phrase string) int {
	for k := min(len(phrase)-1, len(text)); k > 0; k-- {
		if strings.EqualFold(text[len(text)-k:], phrase[:k]) {
			return k
		}
	}
	return 0
}
