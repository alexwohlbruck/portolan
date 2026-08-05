package stages

// Tuning carries the workbench dials into the stages. The pipeline sets it
// once before a run (runs are serialized by the atlas); every default*Params
// constructor consults it, so a dial added in pipeline.Dials is live here
// with no further wiring. Zero-value Tuning means built-in defaults.
type Tuning map[string]float64

var tune Tuning

// SetTuning installs the dials for the next pipeline run.
func SetTuning(t Tuning) { tune = t }

// dial returns the tuned value for key, or def when unset/zero.
func dial(key string, def float64) float64 {
	if v, ok := tune[key]; ok && v != 0 {
		return v
	}
	return def
}
