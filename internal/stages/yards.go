package stages

import "github.com/alexwohlbruck/portolan/internal/yards"

// yardIx is the yard-region oracle for this run, installed by the
// pipeline before MATCH like the other Set* facts. Every query on a nil
// index answers false/none, so consumers need no guards — the --corridors
// path loads no OSM and per-feed style can opt out. Runs are serialized
// by the atlas (see SetTuning).
var yardIx *yards.Index

func SetYards(ix *yards.Index) { yardIx = ix }
