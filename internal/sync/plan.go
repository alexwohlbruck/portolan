package sync

// The patch planner: given the registry, the state manifest and the
// changed feed keys C, work out the closure of "input set changed"
// (docs/SYNC.md) and say exactly what would rebuild. The executor
// (phase 3) walks the Plan; --dry-run prints it.

import (
	"fmt"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// Plan is one sync run's work order. All lists are sorted feed/group
// keys. Nothing here has happened yet — a Plan is a statement about
// inputs, produced identically by patch and global (patch just measures
// fewer feeds to get there).
type Plan struct {
	Changed  []string // C — feeds whose zip content moved
	Measured []string // feeds whose shapes were read (the bbox-prefiltered closure)
	Affected []string // C ∪ steel neighbors ∪ previous co-members/overlays

	Standalone []string // rebuild their own builds: affected feeds no group absorbs
	// MemberPyramids: changed feeds absorbed by a surviving group — the
	// group draws them in the world, but their per-feed pyramid rebuilds
	// because their own zip changed.
	MemberPyramids []string
	Groups         []string // group builds to run (created + modified + input-changed)
	GroupsCreated  []string
	GroupsDeleted  []string
	// Overlays: wide feeds whose BACKGROUND build rebuilds — their zip
	// changed, or a group window they cede moved under them.
	Overlays []string

	// Widened: feeds whose window was grown to cover their own shapes. A
	// window smaller than the railroad clips it away without an error, so
	// the run says which feeds it corrected.
	Widened []string

	RegistryChanged bool
	// Registry: the rewritten portolan.json payload (exactly what
	// groups.py --write would have produced), nil when nothing moved.
	Registry []byte

	Derivation *Derivation // the measurement, for reporting
}

// PlanOpts wires one planning run.
type PlanOpts struct {
	Config   registry.Config
	Doc      *Obj   // the same file, order-preserved (LoadDoc)
	State    *State // the manifest; the planner reads nothing from it today, the executor stamps it
	Changed  []string
	BuildDir string
	// Global measures every feed with a zip on disk instead of the bbox
	// closure of Changed. With Global, Changed may be nil — it defaults
	// to every measurable feed.
	Global bool
	Log    func(format string, a ...any)
}

// BuildPlan measures, re-derives group membership over the affected
// component(s), diffs against the registry, and returns the Plan.
func BuildPlan(o PlanOpts) (*Plan, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cfg := o.Config

	// C: validated feed keys. A group key is not a feed whose zip can
	// change — its inputs are its members' zips. And a changed feed's
	// zip must be ON DISK: C means "this zip's content moved", and
	// planning group dissolution from an absent zip would read a partial
	// data tree as the railway vanishing.
	for _, k := range o.Changed {
		fc, ok := cfg.Feeds[k]
		if !ok {
			return nil, fmt.Errorf("--feeds %s: not in the registry", k)
		}
		if len(fc.Members) > 0 {
			return nil, fmt.Errorf("--feeds %s: a group, not a feed — name its members", k)
		}
		if zip := fc.PrimaryGTFS(); zip != "" {
			if _, err := os.Stat(zip); err != nil {
				return nil, fmt.Errorf("%s: %s is not on disk — a changed feed's zip must be present (run sync check first)", k, zip)
			}
		}
	}
	changed := dedupSort(o.Changed)

	// measurable: non-group feeds whose primary zip exists
	measurable := map[string]bool{}
	for k, fc := range cfg.Feeds {
		if len(fc.Members) > 0 {
			continue
		}
		zip := fc.PrimaryGTFS()
		if zip == "" {
			continue
		}
		if _, err := os.Stat(zip); err == nil {
			measurable[k] = true
		}
	}

	var only map[string]bool
	if o.Global {
		if len(changed) == 0 {
			for k := range measurable {
				changed = append(changed, k)
			}
			sort.Strings(changed)
		}
	} else {
		only = closureByWindow(cfg, measurable, changed)
		logf("bbox prefilter: measuring %d of %d feeds", len(only), len(measurable))
	}

	der, err := DeriveGroups(cfg, only, o.BuildDir, logf)
	if err != nil {
		return nil, err
	}

	inC := map[string]bool{}
	for _, k := range changed {
		inC[k] = true
	}

	// affected: C, plus feeds sharing steel with C now, plus everyone a
	// pre-existing group ties to a feed in C (a feed can LEAVE a group
	// only because a zip in that group changed, and the leaver must then
	// get a standalone build).
	affected := map[string]bool{}
	for k := range inC {
		affected[k] = true
	}
	for p := range der.Pairs {
		if inC[p[0]] {
			affected[p[1]] = true
		}
		if inC[p[1]] {
			affected[p[0]] = true
		}
	}
	for _, fc := range cfg.Feeds {
		if len(fc.Members) == 0 {
			continue
		}
		tied := append(append([]string(nil), fc.Members...), fc.Overlays...)
		touches := false
		for _, m := range tied {
			if inC[m] {
				touches = true
				break
			}
		}
		if touches {
			for _, m := range tied {
				affected[m] = true
			}
		}
	}

	// group-scope derivation: only the derived groups touching an
	// affected feed take part in the diff — the rest of the world is
	// out of scope for a patch (and identical anyway for global).
	scoped := &Derivation{
		Length: der.Length, Pairs: der.Pairs, Extent: der.Extent,
		Duplicate: der.Duplicate, Undrawn: der.Undrawn, Measured: der.Measured,
	}
	// scope bounds the delete rule: a derived group is deletable only if
	// its members were measured. For patch that is the affected
	// component; for global it is every feed with a zip on disk — a
	// derived group whose members were never downloaded is out of scope,
	// not unsupported (global operates on what is local, and an absent
	// data tree must not read as the railway vanishing).
	scope := measurable
	if !o.Global {
		scope = affected
	}
	for _, g := range der.Groups {
		if scope == nil {
			scoped.Groups = append(scoped.Groups, g)
			continue
		}
		for _, m := range g.Members {
			if scope[m] {
				scoped.Groups = append(scoped.Groups, g)
				break
			}
		}
	}

	// rewrite on a clone; diff the group entries entry-by-entry
	before := o.Doc
	after := o.Doc.Clone()
	kept := RewriteGroups(after, scoped, scope)
	// A feed's window is also its shape clip, so one smaller than the feed's
	// own railroad truncates the map silently. Widen before the entries are
	// diffed, so a widened feed is dirty and rebuilds.
	widened := WidenFeedWindows(after, der, scope)

	beforeFeeds := feedsObj(before)
	afterFeeds := feedsObj(after)
	entryBytes := func(f *Obj, k string) []byte {
		if v, ok := f.Get(k); ok {
			return MarshalDoc(v)
		}
		return nil
	}
	plan := &Plan{Changed: changed, Measured: der.Measured, Derivation: der, Widened: widened}
	keptSet := map[string]bool{}
	groupInputs := map[string][]string{} // key → member+overlay feeds (kept is 1:1 with scoped.Groups)
	for i, k := range kept {
		keptSet[k] = true
		g := scoped.Groups[i]
		groupInputs[k] = append(groupInputs[k], g.Members...)
		groupInputs[k] = append(groupInputs[k], g.Overlays...)
	}
	groupRebuild := map[string]bool{}
	for k := range keptSet {
		b := entryBytes(beforeFeeds, k)
		switch {
		case b == nil:
			plan.GroupsCreated = append(plan.GroupsCreated, k)
			groupRebuild[k] = true
			plan.RegistryChanged = true
		case string(b) != string(entryBytes(afterFeeds, k)):
			groupRebuild[k] = true
			plan.RegistryChanged = true
		default:
			// unchanged shape — rebuilds only when an input zip changed
			for _, m := range groupInputs[k] {
				if inC[m] {
					groupRebuild[k] = true
					break
				}
			}
		}
	}
	// deleted: derived entries in scope that the data no longer supports
	for _, k := range beforeFeeds.Keys() {
		if keptSet[k] || afterFeeds.Has(k) {
			continue
		}
		plan.GroupsDeleted = append(plan.GroupsDeleted, k)
		plan.RegistryChanged = true
	}
	sort.Strings(plan.GroupsDeleted)
	if plan.RegistryChanged {
		plan.Registry = MarshalDoc(after)
	}

	// roles after the rewrite
	memberAfter := map[string]bool{}
	overlayAfter := map[string]bool{}
	for _, g := range scoped.Groups {
		for _, m := range g.Members {
			memberAfter[m] = true
		}
		for _, ov := range g.Overlays {
			overlayAfter[ov] = true
		}
	}
	// members/overlays of untouched surviving groups still count
	for _, k := range afterFeeds.Keys() {
		e, _ := afterFeeds.Get(k)
		eo, ok := e.(*Obj)
		if !ok || !eo.Has("members") {
			continue
		}
		for _, m := range strsOf(eo, "members") {
			memberAfter[m] = true
		}
		for _, ov := range strsOf(eo, "overlays") {
			overlayAfter[ov] = true
		}
	}
	overlayBefore := map[string]bool{}
	for _, k := range beforeFeeds.Keys() {
		e, _ := beforeFeeds.Get(k)
		if eo, ok := e.(*Obj); ok {
			for _, ov := range strsOf(eo, "overlays") {
				overlayBefore[ov] = true
			}
		}
	}

	// overlay backgrounds: zip changed, or the exclude-window set moved
	overlaySet := map[string]bool{}
	for ov := range overlayAfter {
		if inC[ov] {
			overlaySet[ov] = true
			continue
		}
		if excludeWindows(beforeFeeds, ov) != excludeWindows(afterFeeds, ov) {
			overlaySet[ov] = true
		}
	}
	for ov := range overlayBefore {
		if !overlayAfter[ov] && (inC[ov] ||
			excludeWindows(beforeFeeds, ov) != excludeWindows(afterFeeds, ov)) {
			overlaySet[ov] = true // it ceded windows it no longer cedes
		}
	}

	for k := range affected {
		plan.Affected = append(plan.Affected, k)
		if memberAfter[k] {
			if inC[k] {
				plan.MemberPyramids = append(plan.MemberPyramids, k)
			}
			continue
		}
		if overlayAfter[k] || overlayBefore[k] {
			continue // background rebuilds ride the overlay rule
		}
		plan.Standalone = append(plan.Standalone, k)
	}
	sort.Strings(plan.Affected)
	sort.Strings(plan.Standalone)
	sort.Strings(plan.MemberPyramids)
	for k := range groupRebuild {
		plan.Groups = append(plan.Groups, k)
	}
	sort.Strings(plan.Groups)
	sort.Strings(plan.GroupsCreated)
	for ov := range overlaySet {
		plan.Overlays = append(plan.Overlays, ov)
	}
	sort.Strings(plan.Overlays)
	return plan, nil
}

// closureByWindow: the bbox prefilter (docs/SYNC.md) — only feeds whose
// window intersects a changed feed's window are measured, transitively
// through REGION-scale windows, so a whole shared-steel component is
// always measured together (two feeds sharing steel have overlapping
// windows, and a member chain is a window chain). Corridor-scale
// windows (area > maxMemberArea: Amtrak, VIA) are measured when touched
// but do not propagate the frontier — they can never be members, and
// letting them propagate would weld every patch into a global run. A
// changed feed always propagates, whatever its size. A feed without a
// configured bbox has an unknown window: it intersects everything and
// propagates (this registry has none).
func closureByWindow(cfg registry.Config, measurable map[string]bool,
	changed []string) map[string]bool {
	win := map[string]*Extent{}
	for k := range measurable {
		if b := cfg.Feeds[k].BBox; len(b) == 4 {
			win[k] = &Extent{b[0], b[1], b[2], b[3]}
		} else {
			win[k] = nil // unknown window: intersects everything
		}
	}
	inC := map[string]bool{}
	for _, k := range changed {
		inC[k] = true
	}
	propagates := func(k string) bool {
		return inC[k] || win[k] == nil || win[k].Area() <= maxMemberArea
	}
	hits := func(a, b *Extent) bool {
		if a == nil || b == nil {
			return true
		}
		return a.Intersects(*b)
	}
	s := map[string]bool{}
	for _, k := range changed {
		if measurable[k] {
			s[k] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for k := range measurable {
			if s[k] {
				continue
			}
			for in := range s {
				if propagates(in) && hits(win[k], win[in]) {
					s[k] = true
					grew = true
					break
				}
			}
		}
	}
	return s
}

// excludeWindows: the serialized, order-free set of group bboxes that
// cut territory out of an overlay's background build — the exclude-bbox
// list feed.sh derives. Two equal strings mean the overlay's windows
// did not move.
func excludeWindows(feeds *Obj, overlay string) string {
	var wins []string
	for _, k := range feeds.Keys() {
		v, _ := feeds.Get(k)
		e, ok := v.(*Obj)
		if !ok {
			continue
		}
		found := false
		for _, ov := range strsOf(e, "overlays") {
			if ov == overlay {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if b, ok := e.Get("bbox"); ok {
			wins = append(wins, string(MarshalDoc(b)))
		}
	}
	sort.Strings(wins)
	return fmt.Sprint(wins)
}

func feedsObj(doc *Obj) *Obj {
	if v, ok := doc.Get("feeds"); ok {
		if o, ok := v.(*Obj); ok {
			return o
		}
	}
	return NewObj()
}

func strsOf(e *Obj, field string) []string {
	v, _ := e.Get(field)
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func dedupSort(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
