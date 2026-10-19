package proc

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func wantLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLineSplitter(t *testing.T) {
	t.Run("one chunk, several lines", func(t *testing.T) {
		wantLines(t, NewLineSplitter(MaxLineBytes).Push([]byte("a\nbb\nccc\n")), "a", "bb", "ccc")
	})

	t.Run("a line cut in the middle is joined", func(t *testing.T) {
		s := NewLineSplitter(MaxLineBytes)
		wantLines(t, s.Push([]byte(`{"a":`)))
		wantLines(t, s.Push([]byte("1}\n{")), `{"a":1}`)
		wantLines(t, s.Push([]byte(`"b":2}`+"\n")), `{"b":2}`)
	})

	t.Run("a chunk may end exactly on a newline, or start with one", func(t *testing.T) {
		s := NewLineSplitter(MaxLineBytes)
		wantLines(t, s.Push([]byte("x\n")), "x")
		wantLines(t, s.Push([]byte("\ny")), "")
		wantLines(t, s.Push([]byte("\n")), "y")
	})

	t.Run("a multi-byte character split across chunks stays whole", func(t *testing.T) {
		data := []byte("xin chào \U0001F600\n")
		for cut := 1; cut < len(data)-1; cut++ {
			s := NewLineSplitter(MaxLineBytes)
			lines := append(s.Push(data[:cut]), s.Push(data[cut:])...)
			wantLines(t, lines, "xin chào \U0001F600")
		}
	})

	t.Run("a carriage return before the newline is dropped", func(t *testing.T) {
		wantLines(t, NewLineSplitter(MaxLineBytes).Push([]byte("a\r\nb\n")), "a", "b")
	})

	t.Run("an unterminated tail is held back, never emitted", func(t *testing.T) {
		s := NewLineSplitter(MaxLineBytes)
		wantLines(t, s.Push([]byte("done\npartial")), "done")
		wantLines(t, s.Push(nil))
	})

	t.Run("a 12 MiB line in 64 KiB chunks comes out as one line", func(t *testing.T) {
		line := strings.Repeat("x", 12*1024*1024)
		data := []byte(line + "\nnext\n")
		s := NewLineSplitter(MaxLineBytes)
		var out []string
		for i := 0; i < len(data); i += 65536 {
			out = append(out, s.Push(data[i:min(i+65536, len(data))])...)
		}
		if len(out) != 2 || out[0] != line || out[1] != "next" {
			t.Fatalf("got %d lines", len(out))
		}
	})

	t.Run("a line at the cap goes through", func(t *testing.T) {
		s := NewLineSplitter(8)
		wantLines(t, s.Push([]byte("1234")))
		wantLines(t, s.Push([]byte("5678\nnext\n")), "12345678", "next")
		if s.TooLong() {
			t.Fatal("a line at the cap ended the stream")
		}
	})

	t.Run("a line past the cap ends the stream: the lines before it come out, nothing after, nothing is held", func(t *testing.T) {
		s := NewLineSplitter(8)
		wantLines(t, s.Push([]byte("ok\n123456789\nlater\n")), "ok")
		if !s.TooLong() {
			t.Fatal("the stream did not end")
		}
		wantLines(t, s.Push([]byte("more\n")))
		if !s.TooLong() {
			t.Fatal("the stream came back")
		}
		if len(s.pending) != 0 {
			t.Fatal("the splitter holds bytes after the end")
		}
	})

	t.Run("an unterminated tail past the cap ends the stream before its newline arrives", func(t *testing.T) {
		s := NewLineSplitter(8)
		wantLines(t, s.Push([]byte("12345")))
		if s.TooLong() {
			t.Fatal("ended too early")
		}
		wantLines(t, s.Push([]byte("6789")))
		if !s.TooLong() {
			t.Fatal("the tail past the cap did not end the stream")
		}
		wantLines(t, s.Push([]byte("\n")))
	})

	t.Run("the default cap holds far more than a real ACP line", func(t *testing.T) {
		if MaxLineBytes < 10*1024*1024 {
			t.Fatalf("MaxLineBytes is %d", MaxLineBytes)
		}
		s := NewLineSplitter(MaxLineBytes)
		wantLines(t, s.Push(bytes.Repeat([]byte("x"), MaxLineBytes+1)))
		if !s.TooLong() {
			t.Fatal("a line past the cap did not end the stream")
		}
	})

	t.Run("the chunk may be reused by the caller", func(t *testing.T) {
		s := NewLineSplitter(MaxLineBytes)
		buf := []byte("abc")
		wantLines(t, s.Push(buf))
		copy(buf, "xyz")
		wantLines(t, s.Push([]byte("\n")), "abc")
	})
}
