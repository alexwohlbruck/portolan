package pipeline

// EXPORT: the modified GTFS half of a region build. MATCH has already
// worked out where every pattern's track actually runs — the wobbly
// shapes.txt polyline snapped onto OSM rail, cleaned, and healed across
// gaps. Exporting writes each SOURCE feed back out as a zip whose
// shapes.txt carries that matched geometry, so the adjusted feeds remain
// valid GTFS (trips.txt still references the same shape ids) and any
// downstream consumer sees lines that sit on real track.
//
// Only whole shapes are replaced: a pattern clipped at the region bbox
// ("#clipN" suffix) is a partial walk of its shape, and stitching a
// half-matched shape onto its untouched remainder would kink at the seam.
// Those shapes keep their original rows, and the count is reported.

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// exportGTFS writes one adjusted zip per source feed into dir. gtfsList is
// ChartOpts.GTFS verbatim — the comma order defines the f<i>: route
// prefixes, which is how each path finds its way home.
func exportGTFS(dir, gtfsList string, paths []stages.Path, frame geo.Frame,
	logf func(string, ...any)) error {

	srcs := strings.Split(gtfsList, ",")
	// (feed index, shape id) → matched geometry; first path wins when two
	// routes ride one shape — MATCH is deterministic per pattern, so any
	// later duplicate is the same walk again.
	type key struct {
		feed  int
		shape string
	}
	geom := map[key][]geo.LL{}
	clipped := map[key]bool{}
	for _, p := range paths {
		fi := feedIndexOf(p.Pattern.Route.ID)
		if fi >= len(srcs) {
			continue
		}
		sid := p.Pattern.ShapeID
		if base, _, isClip := strings.Cut(sid, "#clip"); isClip {
			clipped[key{fi, base}] = true
			continue
		}
		if base, _, isExc := strings.Cut(sid, "#exc"); isExc {
			clipped[key{fi, base}] = true
			continue
		}
		k := key{fi, sid}
		if _, ok := geom[k]; ok || p.Line == nil || len(p.Line.Pts) < 2 {
			continue
		}
		lls := make([]geo.LL, len(p.Line.Pts))
		for i, pt := range p.Line.Pts {
			lls[i] = frame.ToLL(pt)
		}
		geom[k] = lls
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for fi, src := range srcs {
		src = strings.TrimSpace(src)
		shapes := map[string][]geo.LL{}
		nclip := 0
		for k, g := range geom {
			if k.feed == fi {
				shapes[k.shape] = g
			}
		}
		for k := range clipped {
			if k.feed == fi {
				nclip++
			}
		}
		out := filepath.Join(dir, filepath.Base(src))
		if err := rewriteZip(src, out, shapes); err != nil {
			return fmt.Errorf("export %s: %w", src, err)
		}
		logf("export: %s — %d shapes adjusted, %d clipped shapes kept as-is",
			out, len(shapes), nclip)
	}
	return nil
}

// feedIndexOf recovers which source zip a route came from: loadFeeds
// prefixes overlay routes "f<i>:", and the base feed rides unprefixed.
func feedIndexOf(routeID string) int {
	if !strings.HasPrefix(routeID, "f") {
		return 0
	}
	pre, _, ok := strings.Cut(routeID[1:], ":")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(pre)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// rewriteZip copies src to dst replacing shapes.txt rows for the shapes
// given; every other entry passes through byte-identical. A feed with no
// shapes.txt gains one — pfaedle-less feeds exist, and the matched walk
// is strictly better than nothing.
func rewriteZip(src, dst string, shapes map[string][]geo.LL) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	sawShapes := false
	for _, e := range zr.File {
		name := filepath.Base(e.Name)
		r, err := e.Open()
		if err != nil {
			return err
		}
		w, werr := zw.Create(e.Name)
		if werr != nil {
			r.Close()
			return werr
		}
		if name == "shapes.txt" {
			sawShapes = true
			err = filterShapes(r, w, shapes)
		} else {
			_, err = io.Copy(w, r)
		}
		r.Close()
		if err != nil {
			return err
		}
	}
	if !sawShapes && len(shapes) > 0 {
		w, err := zw.Create("shapes.txt")
		if err != nil {
			return err
		}
		if err := filterShapes(strings.NewReader(
			"shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence\n"), w, shapes); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// filterShapes streams shapes.txt: rows of replaced shapes are dropped,
// everything else copies through, and the new geometry is appended in the
// file's own column order. shape_dist_traveled is left empty on written
// rows — the distances of the old polyline would be lies about the new.
func filterShapes(r io.Reader, w io.Writer, shapes map[string][]geo.LL) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return fmt.Errorf("shapes.txt header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	idc, ok1 := col["shape_id"]
	latc, ok2 := col["shape_pt_lat"]
	lonc, ok3 := col["shape_pt_lon"]
	seqc, ok4 := col["shape_pt_sequence"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return fmt.Errorf("shapes.txt lacks required columns")
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(head); err != nil {
		return err
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if idc < len(rec) {
			if _, replaced := shapes[rec[idc]]; replaced {
				continue
			}
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	for sid, pts := range shapes {
		for i, ll := range pts {
			rec := make([]string, len(head))
			rec[idc] = sid
			rec[latc] = strconv.FormatFloat(ll.Lat, 'f', 6, 64)
			rec[lonc] = strconv.FormatFloat(ll.Lon, 'f', 6, 64)
			rec[seqc] = strconv.Itoa(i)
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
