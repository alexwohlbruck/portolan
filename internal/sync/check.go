package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// CheckOpts wires one check run: the registry says which feeds exist, the
// manifest at StatePath says what we last saw, the client asks upstream.
type CheckOpts struct {
	Config    registry.Config
	StatePath string
	DataDir   string // downloaded zips land here as <feedkey>.zip
	Client    *Client
	DryRun    bool // stop after the diff: no downloads, no state writes
	Log       func(format string, a ...any)
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// CheckResult is the machine-readable outcome — the RESULT line's body.
type CheckResult struct {
	Changed []string `json:"changed"` // feeds whose content actually moved
	Skipped []string `json:"skipped"` // feeds with no onestop id
	Errors  []string `json:"errors"`  // "feed: what went wrong", run continued
}

// Check asks transitland for every registered feed's current version sha,
// diffs against the manifest, and downloads what moved into DataDir. A
// changed sha over identical content records the new sha but does not
// count as changed — the content hash is the identity that matters. The
// manifest is saved after each feed, so an interrupted run resumes where
// it stopped. One feed's failure lands in Errors; the run carries on.
func Check(o CheckOpts) (CheckResult, error) {
	res := CheckResult{Changed: []string{}, Skipped: []string{}, Errors: []string{}}
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	st, err := LoadState(o.StatePath)
	if err != nil {
		return res, err
	}

	keys := make([]string, 0, len(o.Config.Feeds))
	for k := range o.Config.Feeds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	oops := func(key string, err error) {
		res.Errors = append(res.Errors, key+": "+err.Error())
		logf("%s: %v", key, err)
	}
	for _, key := range keys {
		fc := o.Config.Feeds[key]
		if fc.Onestop == "" {
			res.Skipped = append(res.Skipped, key)
			logf("%s: no onestop id — skipped", key)
			continue
		}
		info, err := o.Client.Feed(fc.Onestop)
		if err != nil {
			oops(key, err)
			continue
		}
		if info.SHA1 == "" {
			oops(key, fmt.Errorf("%s: no feed version upstream", fc.Onestop))
			continue
		}
		prev := st.Feeds[key]
		if info.SHA1 == prev.SHA1 {
			logf("%s: unchanged (%s)", key, short(info.SHA1))
			continue
		}
		if o.DryRun {
			res.Changed = append(res.Changed, key)
			logf("%s: upstream %s, have %s — would download", key, short(info.SHA1), short(prev.SHA1))
			continue
		}
		dest := filepath.Join(o.DataDir, key+".zip")
		tmp := dest + ".sync"
		if err := o.Client.Download(fc.Onestop, info.StaticCurrent, tmp); err != nil {
			oops(key, err)
			continue
		}
		content, err := ContentHash(tmp)
		if err != nil {
			os.Remove(tmp)
			oops(key, err)
			continue
		}
		cur := prev
		cur.Onestop = fc.Onestop
		cur.SHA1 = info.SHA1
		if content == prev.Content && prev.Content != "" {
			// a republish: new sha, same tables — record the sha so the
			// next check is quiet, but nothing downstream needs to move
			os.Remove(tmp)
			logf("%s: new sha %s over identical content — recorded", key, short(info.SHA1))
		} else {
			if err := os.Rename(tmp, dest); err != nil {
				os.Remove(tmp)
				oops(key, err)
				continue
			}
			cur.Content = content
			res.Changed = append(res.Changed, key)
			logf("%s: changed → %s", key, dest)
		}
		st.Feeds[key] = cur
		if err := st.Save(o.StatePath); err != nil {
			return res, err
		}
	}
	if !o.DryRun {
		st.LastCheck = now().UTC().Format(time.RFC3339)
		if err := st.Save(o.StatePath); err != nil {
			return res, err
		}
	}
	return res, nil
}

func short(sha string) string {
	if sha == "" {
		return "nothing"
	}
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
