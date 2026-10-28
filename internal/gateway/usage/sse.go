package usage

import "bytes"

// maxLine caps one buffered SSE line. Longer lines are skipped, never grown
// without bound, so a hostile or broken upstream cannot exhaust memory.
const maxLine = 4 << 20

// Event is one parsed server-sent event.
type Event struct {
	Name string // the "event:" field, "" if absent
	Data []byte // the joined "data:" lines
}

// splitter turns an arbitrary byte stream into SSE events incrementally. It
// is pure and tolerant: malformed input yields fewer events, never a panic.
type splitter struct {
	buf      []byte
	skipping bool // inside an over-long line
	name     string
	data     []byte
	hasData  bool
	emit     func(Event)
}

func (s *splitter) Write(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if !s.skipping {
				if len(s.buf)+len(p) > maxLine {
					s.skipping, s.buf = true, s.buf[:0]
				} else {
					s.buf = append(s.buf, p...)
				}
			}
			return
		}
		if s.skipping {
			s.skipping = false
		} else if len(s.buf)+i <= maxLine {
			s.buf = append(s.buf, p[:i]...)
			s.line(s.buf)
		}
		s.buf = s.buf[:0]
		p = p[i+1:]
	}
}

// Flush handles a final event that lacked its terminating blank line.
func (s *splitter) Flush() {
	if len(s.buf) > 0 && !s.skipping {
		s.line(s.buf)
		s.buf = s.buf[:0]
	}
	s.dispatch()
}

func (s *splitter) line(l []byte) {
	l = bytes.TrimSuffix(l, []byte("\r"))
	if len(l) == 0 {
		s.dispatch()
		return
	}
	if l[0] == ':' {
		return // comment
	}
	field, value, _ := bytes.Cut(l, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(field) {
	case "event":
		s.name = string(value)
	case "data":
		if s.hasData {
			s.data = append(s.data, '\n')
		}
		s.data = append(s.data, value...)
		s.hasData = true
	}
}

func (s *splitter) dispatch() {
	if s.hasData {
		s.emit(Event{Name: s.name, Data: append([]byte(nil), s.data...)})
	}
	s.name, s.data, s.hasData = "", s.data[:0], false
}
