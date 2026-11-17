package records

import (
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// said fills in what a model request's new messages say (convo's New): the
// text of the person's and the harness's messages in order, each harness note
// named by convo's markers (so the pipeline and the Interceptor agree on
// who's speaking; "system" for a system message with none), the person's
// words (the prompt and its whole length), whether the harness says the turn
// before was interrupted, and whether it's a side request for a title or a
// suggested next prompt.
func said(d *events.CallDetail, r convo.Request) {
	var words []string
	for _, i := range r.New {
		if i < 0 || i >= len(r.Messages) {
			continue
		}
		m := r.Messages[i]
		if m.Role != convo.RoleUser && m.Role != convo.RoleSystem {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != convo.KindText {
				continue
			}
			text := strings.TrimSpace(string(p.Content()))
			if text == "" {
				continue
			}
			part := events.SaidPart{Role: string(m.Role), Text: events.SaidText(text)}
			for _, n := range convo.InterruptNotes {
				if strings.HasPrefix(text, n) {
					d.Interrupted, part.Note = true, n
				}
			}
			if m.Role == convo.RoleSystem && part.Note == "" {
				if part.Note = convo.Note(text); part.Note == "" {
					part.Note = "system"
				}
				switch part.Note {
				case convo.TitleNote:
					d.Side = "title"
				case convo.SuggestionNote:
					d.Side = "suggestion"
				}
			}
			if part.Note == "" {
				words = append(words, text)
			}
			d.Said = append(d.Said, part)
		}
	}
	if len(words) > 0 {
		d.SetPrompt(strings.Join(words, "\n"))
	}
}
