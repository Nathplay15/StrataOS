package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Options configure the engine.
type Options struct {
	StateFile string                  // the state database
	Device    string                  // this computer's name, in conflict copies
	Ignore    []string                // name patterns never synchronized (DefaultIgnore when nil)
	Trash     func(path string) error // local deletions (the OS trash)
	OnEvent   func(Event)             // conflicts, errors, the deletion brake
	// The deletion brake: a round deleting at least BrakeMin files and more
	// than BrakeRatio of the files synchronized waits for the user.
	BrakeMin   int
	BrakeRatio float64
	// Interval between full rounds when nothing is reported (5 minutes,
	// 1 minute on macOS where file contents are not watched).
	Interval time.Duration
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// Engine synchronizes the pairs.
type Engine struct {
	opt    Options
	remote Remote
	st     *store

	mu        sync.Mutex
	pairs     map[string]*pairRun
	paused    bool
	signedOut bool
	runCtx    context.Context
}

// Brake describes deletions waiting for the user's decision.
type Brake struct {
	LocalDeletes  int       `json:"localDeletes"`  // files the NAS deleted, to delete here
	RemoteDeletes int       `json:"remoteDeletes"` // files deleted here, to delete on the NAS
	Files         int       `json:"files"`         // files synchronized
	Since         time.Time `json:"since"`
}

// Skipped is a path that is not synchronized, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// PairStatus is the state of a pair, for the interface.
type PairStatus struct {
	ID        string    `json:"id"`
	Local     string    `json:"local"`
	Remote    string    `json:"remote"`
	State     string    `json:"state"` // idle, syncing, paused, error, brake
	Error     string    `json:"error,omitempty"`
	Pending   int       `json:"pending"`
	Done      int       `json:"done"`
	Current   string    `json:"current,omitempty"`
	Progress  float64   `json:"progress,omitempty"` // of the current file, 0…1
	LastSync  time.Time `json:"lastSync,omitzero"`
	Files     int       `json:"files"`
	Conflicts int       `json:"conflicts"`
	Skipped   []Skipped `json:"skipped,omitempty"`
	Links     int       `json:"links,omitempty"`
	Brake     *Brake    `json:"brake,omitempty"`
}

// Status is the state of the engine.
type Status struct {
	Paused    bool         `json:"paused"`
	SignedOut bool         `json:"signedOut"`
	Pairs     []PairStatus `json:"pairs"`
}

type pairRun struct {
	e *Engine
	p Pair
	n namer
	m matcher

	mu      sync.Mutex // one round at a time
	data    pairData
	pending []change

	smu       sync.Mutex
	status    PairStatus
	conflicts int
	brake     *Brake
	allowDel  bool
	restore   bool

	trees map[string][]op // this round: local folders trashed whole, with their content's ops

	kick    chan struct{}
	cancel  context.CancelFunc
	watcher *fsnotify.Watcher
	watched map[string]bool
}

// New opens the state database.
func New(remote Remote, opt Options) (*Engine, error) {
	if opt.Ignore == nil {
		opt.Ignore = DefaultIgnore
	}
	if opt.BrakeMin == 0 {
		opt.BrakeMin = 20
	}
	if opt.BrakeRatio == 0 {
		opt.BrakeRatio = 0.5
	}
	if opt.Interval == 0 {
		opt.Interval = 5 * time.Minute
		if runtime.GOOS == "darwin" {
			opt.Interval = time.Minute
		}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Trash == nil {
		return nil, errors.New("no trash")
	}
	if err := os.MkdirAll(filepath.Dir(opt.StateFile), 0o700); err != nil {
		return nil, err
	}
	st, err := openStore(opt.StateFile)
	if err != nil {
		return nil, fmt.Errorf("state database: %w", err)
	}
	return &Engine{opt: opt, remote: remote, st: st, pairs: map[string]*pairRun{}}, nil
}

// Close stops the pairs and closes the database.
func (e *Engine) Close() error {
	e.mu.Lock()
	for _, pr := range e.pairs {
		pr.stop()
	}
	e.mu.Unlock()
	return e.st.close()
}

func (e *Engine) event(ev Event) {
	if e.opt.OnEvent != nil {
		e.opt.OnEvent(ev)
	}
}

// SetPairs sets the pairs to synchronize: new ones start (when running),
// removed ones stop and their state is forgotten, changed ones restart.
func (e *Engine) SetPairs(pairs []Pair) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	keep := map[string]bool{}
	for _, p := range pairs {
		keep[p.ID] = true
		if old := e.pairs[p.ID]; old != nil {
			if old.p.Local == p.Local && old.p.Remote == p.Remote && strings.Join(old.p.Exclude, "\x00") == strings.Join(p.Exclude, "\x00") && old.p.Paused == p.Paused {
				continue
			}
			old.stop()
			if old.p.Local != p.Local || old.p.Remote != p.Remote {
				_ = e.st.drop(p.ID) // another folder: start from nothing
			}
		}
		pr, err := e.newPair(p)
		if err != nil {
			return err
		}
		e.pairs[p.ID] = pr
		if e.runCtx != nil {
			pr.start(e.runCtx)
		}
	}
	for id, pr := range e.pairs {
		if !keep[id] {
			pr.stop()
			delete(e.pairs, id)
			_ = e.st.drop(id)
		}
	}
	return nil
}

func (e *Engine) newPair(p Pair) (*pairRun, error) {
	d, err := e.st.load(p.ID)
	if err != nil {
		return nil, err
	}
	n := namer{fold: caseInsensitive(p.Local), nfc: runtime.GOOS == "darwin"}
	pr := &pairRun{e: e, p: p, n: n, m: newMatcher(n, e.opt.Ignore, p.Exclude), data: d, kick: make(chan struct{}, 1)}
	pr.status = PairStatus{ID: p.ID, Local: p.Local, Remote: p.Remote, State: "idle"}
	return pr, nil
}

// Pause stops or resumes every transfer.
func (e *Engine) Pause(on bool) {
	e.mu.Lock()
	e.paused = on
	prs := e.list()
	e.mu.Unlock()
	for _, pr := range prs {
		pr.trigger()
	}
}

func (e *Engine) list() []*pairRun {
	var l []*pairRun
	for _, pr := range e.pairs {
		l = append(l, pr)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].p.ID < l[j].p.ID })
	return l
}

func (e *Engine) isPaused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused || e.signedOut
}

// Trigger asks every pair for a round now.
func (e *Engine) Trigger() {
	e.mu.Lock()
	prs := e.list()
	e.mu.Unlock()
	for _, pr := range prs {
		pr.trigger()
	}
}

// ResolveBrake answers the deletion brake of a pair: apply the deletions,
// or restore the files (downloaded or sent again instead).
func (e *Engine) ResolveBrake(id string, apply bool) {
	e.mu.Lock()
	pr := e.pairs[id]
	e.mu.Unlock()
	if pr == nil {
		return
	}
	pr.smu.Lock()
	if apply {
		pr.allowDel = true
	} else {
		pr.restore = true
	}
	pr.smu.Unlock()
	pr.trigger()
}

// Status returns the state of every pair.
func (e *Engine) Status() Status {
	e.mu.Lock()
	s := Status{Paused: e.paused, SignedOut: e.signedOut}
	prs := e.list()
	e.mu.Unlock()
	for _, pr := range prs {
		pr.smu.Lock()
		ps := pr.status
		ps.Conflicts = pr.conflicts
		if pr.brake != nil {
			b := *pr.brake
			ps.Brake = &b
		}
		pr.smu.Unlock()
		s.Pairs = append(s.Pairs, ps)
	}
	return s
}

// Run synchronizes in the background until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	for _, pr := range e.list() {
		pr.start(ctx)
	}
	e.mu.Unlock()
	<-ctx.Done()
	e.mu.Lock()
	for _, pr := range e.pairs {
		pr.stop()
	}
	e.runCtx = nil
	e.mu.Unlock()
}

// SyncOnce makes rounds on every pair until nothing is left (at most a few).
func (e *Engine) SyncOnce(ctx context.Context) error {
	e.mu.Lock()
	prs := e.list()
	e.mu.Unlock()
	var errs []error
	for _, pr := range prs {
		for range 5 {
			again, err := pr.round(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", pr.p.Local, err))
				break
			}
			if !again {
				break
			}
		}
	}
	return errors.Join(errs...)
}

func (pr *pairRun) trigger() {
	select {
	case pr.kick <- struct{}{}:
	default:
	}
}

func (pr *pairRun) setStatus(f func(*PairStatus)) {
	pr.smu.Lock()
	f(&pr.status)
	pr.smu.Unlock()
}

func (pr *pairRun) setCurrent(p string, size, done int64) {
	pr.setStatus(func(s *PairStatus) {
		s.Current = p
		s.Progress = 0
		if size > 0 {
			s.Progress = float64(done) / float64(size)
		}
	})
}

func (pr *pairRun) start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	pr.cancel = cancel
	if w, err := fsnotify.NewWatcher(); err == nil {
		pr.watcher, pr.watched = w, map[string]bool{}
		go pr.watch(ctx, w)
	}
	go pr.poll(ctx)
	go pr.loop(ctx)
}

func (pr *pairRun) stop() {
	if pr.cancel != nil {
		pr.cancel()
		pr.cancel = nil
	}
	pr.mu.Lock() // wait for the round in progress
	if pr.watcher != nil {
		pr.watcher.Close()
		pr.watcher = nil
	}
	pr.mu.Unlock()
}

// loop makes rounds when something changes (debounced), and regularly.
func (pr *pairRun) loop(ctx context.Context) {
	backoff := 5 * time.Second
	for {
		again, err := pr.round(ctx)
		next := pr.e.opt.Interval
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			next = backoff
			backoff = min(backoff*2, 5*time.Minute)
		case again:
			next = time.Second
			backoff = 5 * time.Second
		default:
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-pr.kick:
			// Let a burst of changes end (a folder being copied).
			t := time.NewTimer(1500 * time.Millisecond)
		wait:
			for {
				select {
				case <-pr.kick:
					t.Reset(1500 * time.Millisecond)
				case <-t.C:
					break wait
				case <-ctx.Done():
					return
				}
			}
		case <-time.After(next):
		}
	}
}

// poll waits for changes on the NAS (long poll) and starts a round.
func (pr *pairRun) poll(ctx context.Context) {
	for ctx.Err() == nil {
		pr.mu.Lock()
		cur := pr.data.cursor
		pr.mu.Unlock()
		if cur == "" || pr.e.isPaused() {
			sleep(ctx, 10*time.Second)
			continue
		}
		c, err := pr.e.remote.Changes(ctx, pr.p.Remote, cur, 50*time.Second)
		if err != nil {
			sleep(ctx, 30*time.Second)
			continue
		}
		if c.Reset || len(c.Entries) > 0 || len(c.Deleted) > 0 {
			pr.trigger()
			sleep(ctx, 3*time.Second) // the round moves the cursor on
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// watch follows local changes (folders only: one watch each).
func (pr *pairRun) watch(ctx context.Context, w *fsnotify.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if strings.HasPrefix(filepath.Base(ev.Name), partPrefix) {
				continue
			}
			pr.trigger()
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			pr.trigger() // events lost (overflow): look at everything
		}
	}
}

const maxWatches = 20000

func (pr *pairRun) updateWatches(dirs []string) {
	if pr.watcher == nil {
		return
	}
	now := map[string]bool{}
	for _, d := range dirs {
		now[d] = true
		if !pr.watched[d] && len(pr.watched) < maxWatches {
			if pr.watcher.Add(d) == nil {
				pr.watched[d] = true
			}
		}
	}
	for d := range pr.watched {
		if !now[d] {
			_ = pr.watcher.Remove(d)
			delete(pr.watched, d)
		}
	}
}

// round is one synchronization of the pair. again: more work is waiting.
func (pr *pairRun) round(ctx context.Context) (again bool, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.p.Paused || pr.e.isPaused() {
		pr.setStatus(func(s *PairStatus) { s.State, s.Current = "paused", "" })
		return false, nil
	}
	pr.setStatus(func(s *PairStatus) { s.State, s.Error = "syncing", "" })
	again, err = pr.sync(ctx)
	pr.flush()
	pr.smu.Lock()
	braked := pr.brake != nil
	pr.smu.Unlock()
	pr.setStatus(func(s *PairStatus) {
		s.Current, s.Progress = "", 0
		switch {
		case errors.Is(err, context.Canceled):
			s.State = "idle"
		case err != nil:
			s.State, s.Error = "error", err.Error()
		default:
			s.LastSync = pr.e.opt.Now()
			s.State = "idle"
			if braked {
				s.State = "brake"
			}
		}
	})
	if errors.Is(err, ErrUnauthorized) {
		pr.e.mu.Lock()
		first := !pr.e.signedOut
		pr.e.signedOut = true
		pr.e.mu.Unlock()
		if first {
			pr.e.event(Event{Kind: "signedout", Msg: err.Error()})
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		pr.e.opt.Logf("%s: %v", pr.p.Local, err)
	}
	return again, err
}

// pull follows the change feed of the NAS into the remote tree.
func (pr *pairRun) pull(ctx context.Context) error {
	cursor := pr.data.cursor
	var fresh map[string]*RemoteEntry
	var changes []change
	for {
		c, err := pr.e.remote.Changes(ctx, pr.p.Remote, cursor, 0)
		if err != nil {
			return err
		}
		if c.Reset && fresh == nil {
			fresh = map[string]*RemoteEntry{}
		}
		for i := range c.Entries {
			r := c.Entries[i]
			if fresh != nil {
				fresh[r.Path] = &r
				continue
			}
			pr.data.remote[r.Path] = &r
			changes = append(changes, change{bRemote, r.Path, &r})
		}
		for _, d := range c.Deleted {
			if fresh != nil {
				delete(fresh, d)
				continue
			}
			delete(pr.data.remote, d)
			changes = append(changes, change{bRemote, d, nil})
		}
		cursor = c.Cursor
		if !c.More {
			break
		}
	}
	if fresh != nil {
		pr.data.remote = fresh
		pr.data.cursor = cursor
		return pr.e.st.replaceRemote(pr.p.ID, fresh, cursor)
	}
	pr.data.cursor = cursor
	return pr.e.st.write(pr.p.ID, changes, &cursor)
}

// buildView keys the three trees, leaving out what is not synchronized.
func (pr *pairRun) buildView(sc scanResult) (view, []Skipped) {
	v := view{local: sc.entries, remote: map[string]*RemoteEntry{}, base: map[string]*BaseEntry{}, unreadable: sc.unreadable, localID: pr.localIDOf}
	var skipped []Skipped
	for p, why := range sc.skipped {
		skipped = append(skipped, Skipped{p, why})
	}
	paths := make([]string, 0, len(pr.data.remote))
	for p := range pr.data.remote {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if pr.m.ignored(p) {
			continue
		}
		if why := localProblem(p); why != "" {
			skipped = append(skipped, Skipped{p, why})
			continue
		}
		k := pr.n.key(p)
		if o := v.remote[k]; o != nil {
			skipped = append(skipped, Skipped{p, "another item on the NAS has the same name but for case or accents: " + o.Path})
			continue
		}
		v.remote[k] = pr.data.remote[p]
	}
	for k, b := range pr.data.base {
		if pr.m.ignored(b.Path) {
			pr.forget(k) // excluded since: neither side is touched
			continue
		}
		v.base[k] = b
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	return v, skipped
}

func (pr *pairRun) sync(ctx context.Context) (bool, error) {
	if err := pr.pull(ctx); err != nil {
		return false, err
	}
	sc, err := scanLocal(pr.p.Local, pr.n, pr.m)
	if err != nil {
		return false, err
	}
	pr.updateWatches(sc.dirs)
	v, skipped := pr.buildView(sc)
	pl := makePlan(v)
	pr.fetchHashes(ctx, pl)

	files := 0
	for _, b := range pr.data.base {
		if !b.Dir {
			files++
		}
	}
	ld, rd := pl.deletions()
	pr.smu.Lock()
	allow, restore := pr.allowDel, pr.restore
	pr.allowDel, pr.restore = false, false
	brake := !allow && !restore && ld+rd >= pr.e.opt.BrakeMin && float64(ld+rd) > pr.e.opt.BrakeRatio*float64(files)
	first := brake && pr.brake == nil
	if brake {
		pr.brake = &Brake{LocalDeletes: ld, RemoteDeletes: rd, Files: files, Since: pr.e.opt.Now()}
	} else {
		pr.brake = nil
	}
	pr.smu.Unlock()
	if first {
		pr.e.event(Event{Kind: "brake", Pair: pr.p.ID, Msg: fmt.Sprintf("%d/%d", ld+rd, files)})
	}

	work := 0
	for _, o := range pl.ops {
		if o.kind != opRecord && o.kind != opForget {
			work++
		}
	}
	pr.setStatus(func(s *PairStatus) {
		s.Pending, s.Done, s.Skipped, s.Links = work, 0, skipped, sc.links
		if len(s.Skipped) > 200 {
			s.Skipped = s.Skipped[:200]
		}
	})
	again := pl.again
	conflictsBefore := pr.conflicts
	covered := pr.wholeTrees(pl, sc)
	var firstErr error
	errCount := 0
	for _, o := range pl.ops {
		if ctx.Err() != nil {
			return again, ctx.Err()
		}
		if pr.e.isPaused() {
			return true, nil
		}
		del := o.kind == opDeleteLocal || o.kind == opDeleteRemote
		if del && brake {
			continue
		}
		if del && restore {
			// Restored instead: without a base, the side that still has
			// the file sends it again.
			pr.forget(o.key)
			again = true
			continue
		}
		if covered[o.key] {
			continue // trashed with its folder
		}
		var err error
		if kids, ok := pr.trees[o.key]; ok && o.kind == opDeleteLocal {
			err = pr.deleteTree(o, kids)
		} else {
			err = pr.exec(ctx, o)
		}
		switch {
		case err == nil:
		case errors.Is(err, errSkip):
			again = true
		case errors.Is(err, ErrUnauthorized), errors.Is(err, context.Canceled):
			return again, err
		default:
			errCount++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", o.key, err)
			}
			pr.e.opt.Logf("%s %s: %v", o.kind, o.key, err)
		}
		if o.kind != opRecord && o.kind != opForget {
			pr.setStatus(func(s *PairStatus) { s.Done++ })
		}
	}
	if pr.conflicts != conflictsBefore {
		again = true // the conflict copies go to the NAS at the next round
	}
	pr.setStatus(func(s *PairStatus) {
		n := 0
		for _, b := range pr.data.base {
			if !b.Dir {
				n++
			}
		}
		s.Files = n
	})
	if firstErr != nil {
		if errCount > 1 {
			firstErr = fmt.Errorf("%w (and %d other errors)", firstErr, errCount-1)
		}
		pr.e.event(Event{Kind: "error", Pair: pr.p.ID, Msg: firstErr.Error()})
		return again, firstErr
	}
	return again, nil
}

// fetchHashes asks the NAS for the hashes the comparisons need.
func (pr *pairRun) fetchHashes(ctx context.Context, pl plan) {
	var need []string
	for _, o := range pl.ops {
		if o.kind == opCompare && o.r.Hash == "" {
			need = append(need, o.r.Path)
		}
	}
	for len(need) > 0 {
		n := min(len(need), 500)
		got, err := pr.e.remote.Hashes(ctx, pr.p.Remote, need[:n])
		if err != nil {
			return
		}
		for p, h := range got {
			if r := pr.data.remote[p]; r != nil {
				r.Hash = h
				pr.queue(change{bRemote, p, r})
			}
		}
		need = need[n:]
	}
}

// wholeTrees finds the local folders deleted on the NAS with everything in
// them: they go to the trash whole instead of file by file. It returns the
// ops covered by such a folder.
func (pr *pairRun) wholeTrees(pl plan, sc scanResult) map[string]bool {
	pr.trees = map[string][]op{}
	del := map[string]op{}
	for _, o := range pl.ops {
		if o.kind == opDeleteLocal && o.b != nil {
			del[o.key] = o
		}
	}
	covered := map[string]bool{}
	// Shallowest first, so that a folder inside another is covered by it.
	var dirs []string
	for k, o := range del {
		if o.b.Dir {
			dirs = append(dirs, k)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return depth(dirs[i]) < depth(dirs[j]) })
	for _, d := range dirs {
		if covered[d] {
			continue
		}
		var kids []op
		whole := true
		for k := range sc.entries {
			if !isUnder(k, d) {
				continue
			}
			o, ok := del[k]
			if !ok {
				whole = false // something here stays (new, or changed)
				break
			}
			kids = append(kids, o)
		}
		if !whole {
			continue
		}
		pr.trees[d] = kids
		for _, o := range kids {
			covered[o.key] = true
		}
	}
	return covered
}

// deleteTree trashes a local folder whose whole content was deleted on the
// NAS, after checking every file is still as synchronized.
func (pr *pairRun) deleteTree(d op, kids []op) error {
	for _, o := range kids {
		if o.b.Dir {
			continue
		}
		if cur := statLocal(pr.p.Local, o.b.local()); cur != nil && (cur.Dir || cur.Size != o.b.Size || cur.MTime != o.b.LocalMTime) {
			return errSkip // changed meanwhile: decided file by file next round
		}
	}
	if !pr.onlyJunkOrKnown(d.b.local(), kids) {
		return errSkip
	}
	if err := pr.e.opt.Trash(pr.abs(d.b.local())); err != nil {
		return fmt.Errorf("move to the trash: %w", err)
	}
	for _, o := range kids {
		pr.forget(o.key)
	}
	pr.forget(d.key)
	return nil
}
