package sync

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// DefaultBaseURL is transitland's v2 REST root — the same API
// tools/feed.sh talks to for onboarding.
const DefaultBaseURL = "https://transit.land/api/v2/rest"

// Client talks to transitland. Requests go out sequentially and a 429 is
// honored once (Retry-After, then one retry) — sync polls a fleet, and a
// fleet-sized burst is exactly what the rate limit is for.
type Client struct {
	BaseURL string       // injectable for tests; "" means DefaultBaseURL
	APIKey  string       // appended as ?apikey=; "" sends none
	HTTP    *http.Client // nil means http.DefaultClient
	// Sleep is the 429 backoff, injectable so tests don't wait.
	Sleep func(time.Duration)
}

// NewClient returns a client for the public API with the given key.
func NewClient(apiKey string) *Client { return &Client{APIKey: apiKey} }

// FeedInfo is what check needs from a feed record: the current version's
// identity, and the operator's own URL as a download fallback.
type FeedInfo struct {
	SHA1          string // feed_state.feed_version.sha1
	StaticCurrent string // urls.static_current
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

func (c *Client) withKey(u string) string {
	if c.APIKey == "" {
		return u
	}
	sep := "?"
	if parsed, err := url.Parse(u); err == nil && parsed.RawQuery != "" {
		sep = "&"
	}
	return u + sep + "apikey=" + url.QueryEscape(c.APIKey)
}

// get performs one GET, retrying exactly once on 429 after Retry-After
// (default 1s, capped at 60s). More persistence than that is impoliteness
// with extra steps — the run carries on and the feed lands in errors.
func (c *Client) get(u string) (*http.Response, error) {
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Get(u)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	wait := time.Second
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			wait = time.Duration(secs) * time.Second
		}
	}
	if wait > time.Minute {
		wait = time.Minute
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sleep(wait)
	return httpc.Get(u)
}

// tlFeed mirrors the slice of a transitland feed record check reads.
type tlFeed struct {
	FeedState struct {
		FeedVersion struct {
			SHA1 string `json:"sha1"`
		} `json:"feed_version"`
	} `json:"feed_state"`
	URLs struct {
		StaticCurrent string `json:"static_current"`
	} `json:"urls"`
}

// Feed fetches feeds/{onestop} and returns the current version's sha and
// the static_current fallback URL.
func (c *Client) Feed(onestop string) (FeedInfo, error) {
	u := c.withKey(c.base() + "/feeds/" + url.PathEscape(onestop))
	resp, err := c.get(u)
	if err != nil {
		return FeedInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FeedInfo{}, fmt.Errorf("feeds/%s: HTTP %s", onestop, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return FeedInfo{}, err
	}
	// the list endpoint wraps records in {"feeds":[…]}; a key lookup may
	// return the record bare — accept both rather than betting on one
	var wrapped struct {
		Feeds []tlFeed `json:"feeds"`
	}
	var rec tlFeed
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Feeds) > 0 {
		rec = wrapped.Feeds[0]
	} else if err := json.Unmarshal(raw, &rec); err != nil {
		return FeedInfo{}, fmt.Errorf("feeds/%s: %w", onestop, err)
	}
	return FeedInfo{SHA1: rec.FeedState.FeedVersion.SHA1, StaticCurrent: rec.URLs.StaticCurrent}, nil
}

// Download fetches the feed's current zip to path, atomically (temp file
// beside it, verify it is a zip, rename). download_latest_feed_version
// serves the zip when the licence allows redistribution; otherwise the
// operator's static_current URL is tried — the same two-step
// tools/feed.sh uses.
func (c *Client) Download(onestop, staticCurrent, path string) error {
	primary := c.withKey(c.base() + "/feeds/" + url.PathEscape(onestop) + "/download_latest_feed_version")
	err := c.fetch(primary, path)
	if err == nil {
		return nil
	}
	if staticCurrent == "" {
		return fmt.Errorf("download_latest_feed_version: %w (and no static_current fallback)", err)
	}
	if fbErr := c.fetch(staticCurrent, path); fbErr != nil {
		return fmt.Errorf("download_latest_feed_version: %v; static_current: %w", err, fbErr)
	}
	return nil
}

func (c *Client) fetch(u, path string) error {
	resp, err := c.get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// an HTML error page saved as feed.zip poisons every later stage;
	// refuse anything the zip reader cannot open
	if zr, err := zip.OpenReader(tmp.Name()); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("not a zip: %v", err)
	} else {
		zr.Close()
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
