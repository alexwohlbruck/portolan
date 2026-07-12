package bundle

import (
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Track is one OSM way in the metric frame.
type Track struct {
	ID    string
	Line  *geo.Line
	Layer int // OSM layer tag (stacked tracks still bundle; kept for dials)
}

// Mate is a sustained-parallelism relation between two tracks: the interval
// of A's arc over which B runs alongside. THE kiss rule lives here — two
// tracks bundle only where they stay mutually parallel (offset within
// [MinGap, MaxGap], heading within MinParallel) for at least MinSpan of arc.
// A 30 m brush at a crossing can never create a bundle, by construction.
type Mate struct {
	A, B       int // track indices
	FromA, ToA float64
}

// SoundParams govern stage 2 (soundings) and mateship.
type SoundParams struct {
	SampleStep  float64 // sounding spacing along each track (m)
	MinGap      float64 // closer than this = same track / data dup
	MaxGap      float64 // farther than this = not the same bundle
	MinSpan     float64 // parallelism must be sustained this long (m)
	MinParallel float64 // |cos| heading agreement
}

func DefaultSoundParams() SoundParams {
	return SoundParams{
		SampleStep:  10.0,
		MinGap:      1.0,
		MaxGap:      12.0,
		MinSpan:     60.0,
		MinParallel: 0.82,
	}
}

// Sound computes mate intervals for every track pair via cross-section
// soundings against a shared grid index.
func Sound(tracks []Track, p SoundParams) []Mate {
	lines := make([]*geo.Line, len(tracks))
	for i, t := range tracks {
		lines[i] = t.Line
	}
	grid := geo.NewGrid(lines, 64.0)

	var mates []Mate
	for ai, t := range tracks {
		l := t.Line
		total := l.Len()
		if total < p.MinSpan {
			continue
		}
		n := int(total/p.SampleStep) + 1
		// per neighbor track: sample indices where it runs alongside
		alongside := map[int][]int{}
		for k := 0; k <= n; k++ {
			s := total * float64(k) / float64(n)
			pt := l.AtArc(s)
			tan := l.TangentAtArc(s, p.SampleStep)
			grid.Near(pt, p.MaxGap+2, func(bi int) {
				if bi == ai {
					return
				}
				for _, c := range lines[bi].CrossSection(pt, tan, p.MaxGap+2) {
					off := c.Offset
					if off < 0 {
						off = -off
					}
					if off >= p.MinGap && off <= p.MaxGap && c.Parallel >= p.MinParallel {
						alongside[bi] = append(alongside[bi], k)
						return
					}
				}
			})
		}
		step := total / float64(n)
		for bi, ks := range alongside {
			if bi < ai {
				continue // symmetric; keep one direction
			}
			sort.Ints(ks)
			// sustained runs (allow 1-sample dropouts)
			runFrom := ks[0]
			prev := ks[0]
			flush := func(from, to int) {
				span := float64(to-from) * step
				if span >= p.MinSpan {
					mates = append(mates, Mate{ai, bi, float64(from) * step, float64(to) * step})
				}
			}
			for _, k := range ks[1:] {
				if k-prev > 2 {
					flush(runFrom, prev)
					runFrom = k
				}
				prev = k
			}
			flush(runFrom, prev)
		}
	}
	return mates
}

// Bundle is a group of tracks that ride together, with its computed
// centerline.
//
// v0 GRANULARITY NOTE: bundles are built by union-find over whole tracks.
// OSM ways are short (50–300 m), so whole-way granularity approximates the
// interval graph well in practice — but the real design (docs/ALGORITHM.md
// §3) splits tracks at membership-change points so nodes fall exactly at
// physical forks. That refinement replaces this union-find; do not bolt
// repairs onto it.
type Bundle struct {
	Tracks     []int
	Centerline *geo.Line
}

// Bundles groups mates and computes each group's refined centerline using
// the longest member as the initial spine.
func Bundles(tracks []Track, mates []Mate, p Params) []Bundle {
	parent := make([]int, len(tracks))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for _, m := range mates {
		parent[find(m.A)] = find(m.B)
	}
	groups := map[int][]int{}
	for i := range tracks {
		groups[find(i)] = append(groups[find(i)], i)
	}
	var out []Bundle
	for _, g := range groups {
		spine := g[0]
		for _, ti := range g {
			if tracks[ti].Line.Len() > tracks[spine].Line.Len() {
				spine = ti
			}
		}
		members := make([]*geo.Line, 0, len(g))
		for _, ti := range g {
			members = append(members, tracks[ti].Line)
		}
		cl := Refine(tracks[spine].Line, members, p)
		out = append(out, Bundle{Tracks: g, Centerline: cl})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Centerline.Len() > out[j].Centerline.Len()
	})
	return out
}
