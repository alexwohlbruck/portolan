package sync

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ContentHash fingerprints a GTFS zip by what it says, not how it was
// compressed: sha256 over the *.txt members sorted by name, each framed
// as name, uncompressed size, then bytes. Two zips holding the same
// tables hash the same regardless of member order or compression level —
// how a transitland republish under a new sha is recognized as the same
// feed. Members stream through the hash; nothing is slurped whole.
func ContentHash(zipPath string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", zipPath, err)
	}
	defer zr.Close()

	var members []*zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".txt") {
			members = append(members, f)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

	h := sha256.New()
	for _, f := range members {
		fmt.Fprintf(h, "%s\x00%d\x00", f.Name, f.UncompressedSize64)
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("%s: %s: %w", zipPath, f.Name, err)
		}
		if _, err := io.Copy(h, rc); err != nil {
			rc.Close()
			return "", fmt.Errorf("%s: %s: %w", zipPath, f.Name, err)
		}
		rc.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
