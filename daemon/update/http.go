package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxRedirects is how many redirects one request follows: GitHub's release assets take two (the tag, then the storage host).
const MaxRedirects = 5

// readChunk is how much ReadCapped asks the body for at once; it never holds more than max plus one chunk.
const readChunk = 32 * 1024

// FetchFollowing GETs url, following up to MaxRedirects redirects that RedirectTarget allows. A redirect it refuses,
// or a chain that does not end, is an error. The last response is returned whatever its status; the caller closes its
// Body. A nil client is http.DefaultClient; its own redirect handling is replaced.
func FetchFollowing(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	at := url
	for hop := 0; hop <= MaxRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, at, nil)
		if err != nil {
			return nil, err
		}
		res, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if !IsRedirect(res.StatusCode) {
			return res, nil
		}
		_ = res.Body.Close()
		if at, err = RedirectTarget(at, res.Header.Get("Location")); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("more than %d redirects from %s", MaxRedirects, url)
}

func tooLarge(max int64) error {
	return fmt.Errorf("the body is larger than %d bytes", max)
}

// ReadCapped is the body of res (which it closes), refused once it is larger than max bytes: a Content-Length over it
// before a byte is read, and any body as soon as it passes it, so at most max bytes (and one chunk) are ever held.
func ReadCapped(res *http.Response, max int64) ([]byte, error) {
	defer func() { _ = res.Body.Close() }()
	if res.ContentLength > max {
		return nil, tooLarge(max)
	}
	var body []byte
	buf := make([]byte, readChunk)
	for {
		n, err := res.Body.Read(buf)
		body = append(body, buf[:n]...)
		if int64(len(body)) > max {
			return nil, tooLarge(max)
		}
		if errors.Is(err, io.EOF) {
			return body, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
