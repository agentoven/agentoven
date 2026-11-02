package models

// Trace and span bodies hold whatever users typed and whatever models and tools answered. A reader
// who may see a trace's timings, usage and cost but not its content (Finance and Viewer in Pro,
// ADR-0043) is given the trace with the bodies removed and ContentWithheld set, so a client can say
// so instead of showing an empty prompt.

// WithholdContent removes the bodies from a trace, and from the spans it carries, in place.
func (t *Trace) WithholdContent() {
	if t == nil {
		return
	}
	t.InputText, t.OutputText, t.ThinkingBlocks = "", "", nil
	t.ContentWithheld = true
	for i := range t.Spans {
		t.Spans[i].WithholdContent()
	}
}

// WithholdContent removes the request and response payloads and the event attributes of a span, in place.
func (s *Span) WithholdContent() {
	if s == nil {
		return
	}
	s.Input, s.Output = nil, nil
	if len(s.Events) > 0 {
		events := make([]SpanEvent, len(s.Events))
		for i, e := range s.Events {
			events[i] = SpanEvent{Name: e.Name, Timestamp: e.Timestamp}
		}
		s.Events = events
	}
	s.ContentWithheld = true
}
