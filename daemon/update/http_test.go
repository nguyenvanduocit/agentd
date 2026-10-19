package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// chunks is a body of n chunks of size bytes; served counts the bytes the reader took.
type chunks struct {
	left, size int
	served     *atomic.Int64
}

func (c *chunks) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), c.size)
	for i := range n {
		p[i] = byte(c.left)
	}
	c.left--
	c.served.Add(int64(n))
	return n, nil
}

func (c *chunks) Close() error { return nil }

func response(n, size int, served *atomic.Int64, contentLength int64) *http.Response {
	return &http.Response{Body: &chunks{left: n, size: size, served: served}, ContentLength: contentLength}
}

func TestReadCapped(t *testing.T) {
	t.Run("reads a body of up to the limit, whole", func(t *testing.T) {
		body, err := ReadCapped(response(3, 10, new(atomic.Int64), -1), 30)
		if err != nil || len(body) != 30 {
			t.Fatalf("%d bytes, %v", len(body), err)
		}
		if body[0] != 3 || body[10] != 2 || body[29] != 1 {
			t.Error("the bytes are not in order")
		}
	})
	t.Run("stops reading as soon as the body passes the limit", func(t *testing.T) {
		var served atomic.Int64
		_, err := ReadCapped(response(1_000_000, 1024, &served, -1), 4096)
		if err == nil || !strings.Contains(err.Error(), "larger than 4096 bytes") {
			t.Fatal(err)
		}
		if served.Load() > 4096+readChunk {
			t.Errorf("%d bytes were read", served.Load())
		}
	})
	t.Run("a Content-Length over the limit is refused before a byte is read", func(t *testing.T) {
		var served atomic.Int64
		_, err := ReadCapped(response(1, 10, &served, 999999), 4096)
		if err == nil || !strings.Contains(err.Error(), "larger than 4096 bytes") {
			t.Fatal(err)
		}
		if served.Load() != 0 {
			t.Errorf("%d bytes were read", served.Load())
		}
	})
}

func TestFetchFollowing(t *testing.T) {
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, "arrived") })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	s := httptest.NewServer(mux)
	defer s.Close()

	t.Run("follows a redirect inside the origin", func(t *testing.T) {
		res, err := FetchFollowing(context.Background(), nil, s.URL+"/start")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if string(body) != "arrived" {
			t.Errorf("%q", body)
		}
	})
	t.Run("a redirect to another plain http origin is refused", func(t *testing.T) {
		if _, err := FetchFollowing(context.Background(), nil, s.URL+"/away"); err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Error(err)
		}
	})
	t.Run("a chain that does not end stops after MaxRedirects", func(t *testing.T) {
		hits.Store(0)
		_, err := FetchFollowing(context.Background(), nil, s.URL+"/loop")
		if err == nil || !strings.Contains(err.Error(), "redirects") {
			t.Error(err)
		}
		if hits.Load() != MaxRedirects+1 {
			t.Errorf("%d requests", hits.Load())
		}
	})
	t.Run("the last response is returned whatever its status", func(t *testing.T) {
		res, err := FetchFollowing(context.Background(), nil, s.URL+"/missing")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Error(res.StatusCode)
		}
	})
}
