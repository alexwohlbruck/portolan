package stages

import "github.com/alexwohlbruck/portolan/internal/yards"

// yardIx is the yard-region oracle for this run, installed by the
// pipeline before MATCH like the other Set* facts. Every query on a nil
// index answers false/none, so consumers need no guards — the --corridors
// path loads no OSM and per-feed style can opt out. Runs are serialized
// by the atlas (see SetTuning).
var yardIx *yards.Index

func SetYards(ix *yards.Index) { yardIx = ix }

// isYardWay: TAGGED yard steel (service=yard/siding/spur). Safe against
// ridden track — a tag is a fact about the steel, not about the service
// riding it.
func isYardWay(id string) bool { return yardIx.IsYardWay(id) }

// yardSteel adds untagged hot members of detected regions. This is the
// pool test for UNRIDDEN steel ONLY: a revenue mainline through Sunnyside
// is a region member too, and dropping it from a ridden pool would sever
// a real corridor.
func yardSteel(id string) bool { return yardIx.IsYardWay(id) || yardIx.RegionWay(id) }
