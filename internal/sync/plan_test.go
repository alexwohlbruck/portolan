package sync

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

func TestBuildPlanPatch(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, doc := loadFixture(t, raw)

	plan, err := BuildPlan(PlanOpts{
		Config: cfg, Doc: doc, State: &State{Feeds: map[string]FeedState{}},
		Changed: []string{"b"}, BuildDir: "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	// bbox prefilter: b's window touches a, u and the corridor g; h is
	// reachable only THROUGH the corridor-scale g, which never
	// propagates the frontier; c,d,e,f are far away
	if !reflect.DeepEqual(plan.Measured, []string{"a", "b", "g", "u"}) {
		t.Fatalf("measured = %v", plan.Measured)
	}
	if !reflect.DeepEqual(plan.Affected, []string{"a", "b", "g", "u"}) {
		t.Fatalf("affected = %v", plan.Affected)
	}
	// a,b become a group: created, registry rewritten, b's own pyramid
	if !reflect.DeepEqual(plan.Groups, []string{"alpha-transit"}) ||
		!reflect.DeepEqual(plan.GroupsCreated, []string{"alpha-transit"}) ||
		len(plan.GroupsDeleted) != 0 {
		t.Fatalf("groups = %v created %v deleted %v",
			plan.Groups, plan.GroupsCreated, plan.GroupsDeleted)
	}
	if !plan.RegistryChanged || len(plan.Registry) == 0 {
		t.Fatal("registry rewrite expected")
	}
	// u is affected, absorbed by nothing → standalone; b is a member of
	// the new group → pyramid only; g's exclude windows moved → background
	if !reflect.DeepEqual(plan.Standalone, []string{"u"}) {
		t.Fatalf("standalone = %v", plan.Standalone)
	}
	if !reflect.DeepEqual(plan.MemberPyramids, []string{"b"}) {
		t.Fatalf("member pyramids = %v", plan.MemberPyramids)
	}
	if !reflect.DeepEqual(plan.Overlays, []string{"g"}) {
		t.Fatalf("overlays = %v", plan.Overlays)
	}
	// the patch rewrite must NOT touch the out-of-scope component: h's
	// group is not derived here, and nothing may be deleted for it
	cfg2, err := registry.Parse(plan.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg2.Feeds["alpha-transit"]; !ok {
		t.Fatal("rewritten registry lacks the new group")
	}
	if _, ok := cfg2.Feeds["h"]; !ok {
		t.Fatal("rewritten registry lost an untouched feed")
	}

	// second run over the rewritten registry: the world is settled, so
	// the same change now rebuilds the surviving group and nothing is
	// created, deleted or rewritten
	doc2, err := ParseDoc(plan.Registry)
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPlan(PlanOpts{
		Config: cfg2, Doc: doc2, State: &State{Feeds: map[string]FeedState{}},
		Changed: []string{"b"}, BuildDir: "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.RegistryChanged {
		t.Fatalf("second run rewrote the registry:\n%s", plan2.Registry)
	}
	if !reflect.DeepEqual(plan2.Groups, []string{"alpha-transit"}) ||
		len(plan2.GroupsCreated) != 0 || len(plan2.GroupsDeleted) != 0 {
		t.Fatalf("second run groups = %v created %v deleted %v",
			plan2.Groups, plan2.GroupsCreated, plan2.GroupsDeleted)
	}
	if len(plan2.Overlays) != 0 {
		t.Fatalf("second run overlays = %v — windows did not move", plan2.Overlays)
	}
	if !reflect.DeepEqual(plan2.MemberPyramids, []string{"b"}) {
		t.Fatalf("second run member pyramids = %v", plan2.MemberPyramids)
	}
}

func TestBuildPlanGlobalMatchesPatchRegistry(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, doc := loadFixture(t, raw)

	global, err := BuildPlan(PlanOpts{
		Config: cfg, Doc: doc, State: &State{Feeds: map[string]FeedState{}},
		BuildDir: "build", Global: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// global derives BOTH groups and rewrites both into the registry
	if !reflect.DeepEqual(global.GroupsCreated, []string{"alpha-transit", "hotel-metro"}) {
		t.Fatalf("global created = %v", global.GroupsCreated)
	}
	// patch after b, then patch after h, must land the registry exactly
	// where one global run lands it — the oracle
	p1, err := BuildPlan(PlanOpts{
		Config: cfg, Doc: doc, State: &State{Feeds: map[string]FeedState{}},
		Changed: []string{"b"}, BuildDir: "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg2, _ := registry.Parse(p1.Registry)
	doc2, _ := ParseDoc(p1.Registry)
	p2, err := BuildPlan(PlanOpts{
		Config: cfg2, Doc: doc2, State: &State{Feeds: map[string]FeedState{}},
		Changed: []string{"h"}, BuildDir: "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p2.Registry, global.Registry) {
		t.Fatalf("patch(b)+patch(h) ≠ global:\n%s\n----\n%s", p2.Registry, global.Registry)
	}
}

// TestBuildPlanGlobalKeepsAbsentGroups: global operates on what is
// local — a derived group whose member zips were never downloaded is
// out of scope, not unsupported, and must survive the rewrite.
func TestBuildPlanGlobalKeepsAbsentGroups(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, doc := loadFixture(t, raw)

	// settle the world: derive and rewrite both groups
	d, err := DeriveGroups(cfg, nil, "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	RewriteGroups(doc, d, nil)
	settled := MarshalDoc(doc)
	cfg2, doc2 := loadFixture(t, settled)

	// h's zip vanishes (never downloaded on this machine)
	if err := os.Remove("gtfs/h.zip"); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(PlanOpts{
		Config: cfg2, Doc: doc2, State: &State{Feeds: map[string]FeedState{}},
		BuildDir: "build", Global: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range plan.GroupsDeleted {
		if g == "hotel-metro" {
			t.Fatal("global dissolved a group whose member zips are simply absent")
		}
	}
	reg, err := registry.Parse(settled)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Feeds["hotel-metro"]; !ok {
		t.Fatal("fixture lost hotel-metro before the check")
	}
}
