package sync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// stagingPrefix names a pyramid being built beside its live one. The dot
// keeps it out of every feed-key listing.
const stagingPrefix = ".next-"

// publish swaps the finished pyramid next in for live and drops the old
// one. On Linux the swap is one atomic exchange; elsewhere live is gone for
// the instant between two renames.
func publish(next, live string) error {
	if _, err := os.Stat(live); os.IsNotExist(err) {
		return os.Rename(next, live)
	}
	if exchange(next, live) == nil {
		return os.RemoveAll(next)
	}
	old := filepath.Join(filepath.Dir(live), ".prev-"+filepath.Base(live))
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(live, old); err != nil {
		return err
	}
	if err := os.Rename(next, live); err != nil {
		os.Rename(old, live)
		return err
	}
	return os.RemoveAll(old)
}

func runTileHook(hook, key, next, live string) error {
	cmd := exec.Command("sh", "-c", hook)
	cmd.Env = append(os.Environ(), "PORTOLAN_FEED="+key, "PORTOLAN_TILES="+next, "PORTOLAN_LIVE="+live)
	tail := &tailBuffer{}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v — %s", err, lastLine(tail.bytes()))
	}
	return nil
}
