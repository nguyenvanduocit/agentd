package proc

import "bytes"

// MaxLineBytes is the longest line a splitter keeps, 32 MiB. A line goes on to the server as a JSON string inside one
// Socket.IO message; escaping a JSON line at most doubles it (a backslash before each quote and backslash), so a line
// at the cap still fits the server's 64 MiB message limit (hub.MaxMessageBytes). It is three times the 10 MiB line
// the Go ACP SDK reads and hundreds of times a real one (a large tool result runs to tens of KB).
const MaxLineBytes = 32 * 1024 * 1024

// keepBuffer is the largest buffer a splitter holds on to between lines.
const keepBuffer = 64 * 1024

// LineSplitter cuts a byte stream into lines. A line may arrive in any number of chunks and a chunk may hold any
// number of lines. Bytes are joined before they are decoded, so a multi-byte character that straddles two chunks
// stays whole. A trailing "\r" is dropped, an unterminated tail is held back (it is never emitted).
//
// A line longer than the cap ends the stream: the lines before it are emitted, then TooLong is true and the splitter
// holds and emits nothing more. Cutting the line short would hand on a wrong message, and there is no telling where
// the next one starts.
type LineSplitter struct {
	max     int
	pending []byte
	over    bool
}

// NewLineSplitter makes a splitter that keeps lines of at most max bytes (MaxLineBytes for the daemon).
func NewLineSplitter(max int) *LineSplitter {
	return &LineSplitter{max: max}
}

// TooLong: a line passed the cap; the stream is over.
func (s *LineSplitter) TooLong() bool {
	return s.over
}

// Push takes the next chunk and returns the lines it completed. The chunk is copied: the caller may reuse it.
func (s *LineSplitter) Push(chunk []byte) []string {
	var lines []string
	rest := chunk
	for !s.over {
		nl := bytes.IndexByte(rest, '\n')
		part := rest
		if nl >= 0 {
			part = rest[:nl]
		}
		switch {
		case len(s.pending)+len(part) > s.max:
			s.over = true
			s.pending = nil
		case nl < 0:
			s.pending = append(s.pending, part...)
			return lines
		default:
			s.pending = append(s.pending, part...)
			lines = append(lines, s.take())
			rest = rest[nl+1:]
		}
	}
	return lines
}

func (s *LineSplitter) take() string {
	line := s.pending
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	text := string(line)
	if cap(s.pending) > keepBuffer {
		s.pending = nil
	} else {
		s.pending = s.pending[:0]
	}
	return text
}
