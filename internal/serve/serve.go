// Package serve is portolan as a long-lived process: an interactive
// client posts a corridor graph, watches the stages go by, supersedes
// the build when the user edits something, and fetches the artifacts.
//
//	portolan serve --addr 127.0.0.1:0
//
// The bound port is printed on stdout as the first line, so a
// supervising process can read it back from a :0 request rather than
// guessing a free port.
//
//	POST /chart              → {"id": …}
//	GET  /chart/{id}/progress → SSE: stage name + monotonic 0-100
//	POST /chart/{id}/cancel
//	GET  /chart/{id}/build   → the artifacts
//	GET  /chart/{id}          → job status as JSON
//
// IDLE COST is deliberately nothing: no tickers, no background workers,
// no caches warmed on startup. A job's goroutine exists only while its
// build runs, and finished jobs are reaped when the NEXT job is created
// rather than by a timer.
package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// jobTTL is how long a finished job's artifacts stay fetchable.
const jobTTL = 30 * time.Minute

type event struct {
	Stage string `json:"stage"`
	Pct   int    `json:"pct"`
	Log   string `json:"log,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

type job struct {
	id     string
	dir    string
	out    string
	format string
	cancel context.CancelFunc

	mu       sync.Mutex
	events   []event // replayed to a late subscriber, so nothing is missed
	subs     []chan event
	done     bool
	err      error
	finished time.Time
}

// publish appends an event and fans it out. Subscribers get a buffered
// channel and a non-blocking send: a client that stops reading its SSE
// stream must not be able to wedge the build producing it.
func (j *job) publish(e event) {
	j.mu.Lock()
	j.events = append(j.events, e)
	subs := append([]chan event(nil), j.subs...)
	j.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- e:
		default:
		}
	}
}

func (j *job) subscribe() (<-chan event, []event, func()) {
	c := make(chan event, 64)
	j.mu.Lock()
	past := append([]event(nil), j.events...)
	done := j.done
	j.subs = append(j.subs, c)
	j.mu.Unlock()
	if done {
		close(c)
		return c, past, func() {}
	}
	return c, past, func() {
		j.mu.Lock()
		for i, x := range j.subs {
			if x == c {
				j.subs = append(j.subs[:i], j.subs[i+1:]...)
				break
			}
		}
		j.mu.Unlock()
	}
}

type Server struct {
	styleDir string
	// Version is the binary's release version, reported by /version so a
	// supervising process can check on spawn that the binary speaks the
	// contract its renderer expects, instead of parsing help text.
	Version string
	// Token, when set, is required on every request as
	// `Authorization: Bearer <token>`.
	//
	// The endpoint reads file paths out of the request body — gtfs,
	// style_dir, corridors — so an unauthenticated port is a file-read
	// oracle for any process on the machine, including a plugin running
	// inside the calling application. Binding to loopback is not a
	// boundary between processes on the same host.
	Token string

	mu   sync.Mutex
	jobs map[string]*job

	// buildMu serializes builds, and is the honest limitation of this
	// version.
	//
	// A build's configuration still lives in process-wide state —
	// stages.SetTuning, stages.SetAgencyNames, stages.SetPatternActs,
	// stages.SetFerryRoutes, stages.SetDbg3, stages.SetCrossoverWays,
	// stages.SetWayLevels, stages.SetWayRailClass, mode.SetLineAgencies,
	// mode.SetAgencyNames and style.Set_ — so two builds running at once
	// would read each other's route colours, agency names and dials.
	// Sequential builds of different cities would cross-contaminate the
	// same way.
	//
	// Serializing is therefore correctness, not throughput management,
	// and it must not be removed before that state is threaded through a
	// per-build struct. Cancellation still works while a build waits
	// here: a cancelled job never takes the lock, and one that holds it
	// releases it as soon as ORDER or FAIR notices.
	buildMu sync.Mutex
}

func New(styleDir string) *Server {
	return &Server{styleDir: styleDir, jobs: map[string]*job{}}
}

// ListenAndServe binds addr and serves until the process ends. The bound
// port goes to stdout first, before anything else can write there.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	os.Stdout.Sync()
	return http.Serve(ln, s.mux())
}

func (s *Server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/chart", s.postChart)
	mux.HandleFunc("/chart/", s.jobRoute)
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	return s.authed(mux)
}

// authed enforces the bearer token when one is configured. /healthz and
// /version stay open: a supervisor has to be able to tell "not up yet"
// from "wrong token" while it is still working out how to talk to us,
// and neither reveals anything a caller could not learn by looking at
// the binary it just spawned.
func (s *Server) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" || r.URL.Path == "/healthz" || r.URL.Path == "/version" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// constant time: the token is a secret and a timing oracle on a
		// local port is still an oracle
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="portolan"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// version reports what this binary is and what it speaks, so a caller
// can refuse to draw against a contract it does not understand rather
// than discovering the mismatch in the geometry.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	v := s.Version
	if v == "" {
		v = "devel"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"version": v,
		// the PLNB header's own version field — the number that changes
		// when the binary layout changes, which is the thing a renderer
		// actually has to agree with
		"plnb":    pipeline.BinaryVersion,
		"formats": []string{"geojson", "bin"},
		"bands":   []int{15, 14, 13, 0},
		"auth":    s.Token != "",
	})
}

// Request mirrors the chart verb. Geometry arrives either as a path the
// server can read or, for a client that is editing a network rather
// than storing it, as GeoJSON inline in the request.
type Request struct {
	GTFS          string      `json:"gtfs"`
	GTFSInline    gtfs.Tables `json:"gtfs_inline"`
	Rail          string      `json:"rail"`
	Corridors     string      `json:"corridors"`
	CorridorsJSON interface{} `json:"corridors_inline"`
	CorridorNodes string      `json:"corridor_nodes"`
	Stops         string      `json:"stops"`
	Streets       string      `json:"streets"`
	BBox          []float64   `json:"bbox"`
	LineAgencies  []string    `json:"line_agencies"`
	City          string      `json:"city"`
	StyleDir      string      `json:"style_dir"`
	StyleInline   *style.Doc  `json:"style_inline"`
	Scenario      string      `json:"scenario"`
	Format        string      `json:"format"`
	Band          string      `json:"band"`
	Anchor        []float64   `json:"anchor"` // lat, lon
	Cover         float64     `json:"cover"`
}

func (s *Server) postChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	opts, dir, err := s.optsFor(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.reap()
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: newID(), dir: dir, out: opts.Out, format: req.Format, cancel: cancel}
	s.mu.Lock()
	s.jobs[j.id] = j
	s.mu.Unlock()

	opts.Progress = func(stage string, pct int) {
		// The pipeline's own final step is also called "done", and this
		// job emits a terminal frame of that name below — one carrying
		// the error, which a progress tick cannot. Forwarding both put
		// two indistinguishable "done" stages on the stream. The
		// terminal frame is the one that means it.
		if stage == "done" {
			return
		}
		j.publish(event{Stage: stage, Pct: pct})
	}
	logf := func(f string, a ...any) { j.publish(event{Log: fmt.Sprintf(f, a...)}) }

	go func() {
		defer cancel()
		j.publish(event{Stage: "queued", Pct: 0})
		s.buildMu.Lock()
		err := func() error {
			defer s.buildMu.Unlock()
			// a job cancelled while queued must not start: the client
			// has already superseded it
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return pipeline.ChartCtx(ctx, opts, logf)
		}()
		j.mu.Lock()
		j.done, j.err, j.finished = true, err, time.Now()
		subs := append([]chan event(nil), j.subs...)
		e := event{Stage: "done", Pct: 100, Done: true}
		if err != nil {
			e = event{Stage: "error", Pct: 100, Done: true, Error: err.Error()}
		}
		j.events = append(j.events, e)
		j.subs = nil
		j.mu.Unlock()
		for _, c := range subs {
			select {
			case c <- e:
			default:
			}
			close(c)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"id": j.id})
}

// optsFor turns a request into ChartOpts, writing an inline graph to
// the job's own directory. Every job gets its own directory so two
// builds can never write each other's artifacts.
func (s *Server) optsFor(req *Request) (pipeline.ChartOpts, string, error) {
	var zero pipeline.ChartOpts
	dir, err := os.MkdirTemp("", "portolan-job-")
	if err != nil {
		return zero, "", err
	}
	corridors := req.Corridors
	if req.CorridorsJSON != nil {
		raw, err := json.Marshal(req.CorridorsJSON)
		if err != nil {
			os.RemoveAll(dir)
			return zero, "", fmt.Errorf("corridors_inline: %w", err)
		}
		corridors = filepath.Join(dir, "corridors.geojson")
		if err := os.WriteFile(corridors, raw, 0o644); err != nil {
			os.RemoveAll(dir)
			return zero, "", err
		}
	}
	if (corridors == "") == (req.Rail == "") {
		os.RemoveAll(dir)
		return zero, "", fmt.Errorf("give exactly one of rail or corridors/corridors_inline")
	}
	if req.GTFS == "" && len(req.GTFSInline) == 0 {
		os.RemoveAll(dir)
		return zero, "", fmt.Errorf("gtfs or gtfs_inline is required")
	}
	if len(req.GTFSInline) > 0 {
		if err := req.GTFSInline.Valid(); err != nil {
			os.RemoveAll(dir)
			return zero, "", err
		}
	}
	band, err := pipeline.ParseBand(req.Band)
	if err != nil {
		os.RemoveAll(dir)
		return zero, "", err
	}
	var bandPtr *int
	if req.Band != "" {
		bandPtr = &band
	}
	var anchor *geo.LL
	if len(req.Anchor) == 2 {
		anchor = &geo.LL{Lat: req.Anchor[0], Lon: req.Anchor[1]}
	}
	// Curation is live state for the same callers whose feed tables are:
	// a colour or a bullet shape changes whenever the user edits a line.
	// Path-only would make every such caller write a file per rebuild —
	// and a caller that must write a file needs somewhere writable and a
	// way to reach it, which is a surface rather than a detail. Inline
	// takes the document itself.
	//
	// It REPLACES the directory rather than layering over it. The class
	// defaults are compiled in, so a document alone is complete, and a
	// caller sending one has said what it wants.
	var sty *style.Set
	var dirLas []string
	if req.StyleInline != nil {
		sty = style.New(req.StyleInline.Config())
		dirLas = req.StyleInline.LineAgencies()
	} else {
		styleDir := req.StyleDir
		if styleDir == "" {
			styleDir = s.styleDir
		}
		var err error
		sty, dirLas, err = style.LoadDir(styleDir, req.City)
		if err != nil {
			os.RemoveAll(dir)
			return zero, "", err
		}
	}
	las := req.LineAgencies
	if len(las) == 0 {
		las = dirLas
	}
	d := pipeline.DefaultDials()
	if req.Cover > 0 {
		d.Cover = req.Cover
	}
	name := "build.geojson"
	if req.Format == "bin" {
		name = "build.bin"
	}
	return pipeline.ChartOpts{
		GTFS: req.GTFS, GTFSInline: req.GTFSInline, Rail: req.Rail,
		Corridors: corridors, CorridorNodes: req.CorridorNodes,
		Stops: req.Stops, Streets: req.Streets, BBox: req.BBox,
		LineAgencies: las, Scenario: req.Scenario, Style: sty,
		Anchor: anchor, Format: req.Format, Band: bandPtr,
		Out: filepath.Join(dir, name), Dials: &d,
	}, dir, nil
}

// jobRoute dispatches /chart/{id}[/progress|/cancel|/build].
func (s *Server) jobRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/chart/")
	id, action, _ := strings.Cut(rest, "/")
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		http.Error(w, "no such job", http.StatusNotFound)
		return
	}
	switch action {
	case "progress":
		s.progress(w, r, j)
	case "cancel":
		j.cancel()
		w.WriteHeader(http.StatusNoContent)
	case "build":
		s.build(w, r, j)
	case "":
		j.mu.Lock()
		resp := map[string]any{"id": j.id, "done": j.done}
		if j.err != nil {
			resp["error"] = j.err.Error()
		}
		j.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "no such endpoint", http.StatusNotFound)
	}
}

func (s *Server) progress(w http.ResponseWriter, r *http.Request, j *job) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, past, unsub := j.subscribe()
	defer unsub()
	send := func(e event) {
		raw, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		fl.Flush()
	}
	// replay first: a client that connects after the build started must
	// still see which stages it missed, and the final done event
	for _, e := range past {
		send(e)
	}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			send(e)
			if e.Done {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// build serves an artifact. ?artifact= selects among the files one
// chart writes; the default is the ribbons themselves.
func (s *Server) build(w http.ResponseWriter, r *http.Request, j *job) {
	j.mu.Lock()
	done, err := j.done, j.err
	j.mu.Unlock()
	if !done {
		http.Error(w, "build still running", http.StatusAccepted)
		return
	}
	if err != nil {
		http.Error(w, "build failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	suffix := map[string]string{
		"":            "",
		"segments":    "",
		"stations":    ".stations.geojson",
		"style":       ".style.json",
		"trackcenter": ".trackcenter.geojson",
		"nodes":       ".nodes.geojson",
	}
	sfx, ok := suffix[r.URL.Query().Get("artifact")]
	if !ok {
		http.Error(w, "unknown artifact", http.StatusBadRequest)
		return
	}
	path := j.out + sfx
	if sfx == "" && j.format == "bin" {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	f, ferr := os.Open(path)
	if ferr != nil {
		http.Error(w, "artifact not written", http.StatusNotFound)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(path), time.Time{}, f)
}

// reap drops finished jobs past their TTL, and their directories with
// them. Called when a job is created rather than on a timer, so an idle
// server does no work at all.
func (s *Server) reap() {
	cutoff := time.Now().Add(-jobTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.Lock()
		stale := j.done && j.finished.Before(cutoff)
		j.mu.Unlock()
		if stale {
			os.RemoveAll(j.dir)
			delete(s.jobs, id)
		}
	}
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
