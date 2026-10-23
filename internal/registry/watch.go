package registry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Snapshot is one Scan result plus the errors that came with it.
type Snapshot struct {
	Entries []Entry
	Errs    []error
}

var errDirGone = errors.New("registry watch: sessions dir removed or renamed; re-Watch")

// Watch sends a Snapshot once, then again after each burst of changes to
// *.json files in sessDir (D:129-131). A watcher error forces a rescan, so
// lost events resync. If sessDir itself is removed or renamed, a final
// Snapshot carrying an error is sent and the channel closes: the caller
// re-Watches. The channel also closes when ctx is done.
func Watch(ctx context.Context, sessDir, procRoot string) (<-chan Snapshot, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("registry watch: %w", err)
	}
	// Add before the initial scan so no change falls between the two.
	if err := w.Add(sessDir); err != nil {
		w.Close()
		return nil, fmt.Errorf("registry watch %s: %w", sessDir, err)
	}
	out := make(chan Snapshot)
	go func() {
		defer w.Close()
		watchLoop(ctx, sessDir, procRoot, w.Events, w.Errors, out)
	}()
	return out, nil
}

const (
	// debounce is the quiet time after the last change before a snapshot is
	// offered, so a burst of writes yields one or two snapshots.
	debounce = 20 * time.Millisecond
	// maxLatency bounds the time from the first unsent change to the offer,
	// so continuous writes cannot starve the consumer.
	maxLatency = 200 * time.Millisecond
)

// watchLoop owns out and closes it on return. Channels are parameters so
// tests can inject events and watcher errors.
func watchLoop(ctx context.Context, sessDir, procRoot string, events <-chan fsnotify.Event, errs <-chan error, out chan<- Snapshot) {
	defer close(out)
	dir := filepath.Clean(sessDir)
	gone := func(ev fsnotify.Event) bool {
		return filepath.Clean(ev.Name) == dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0
	}
	quiet := time.NewTimer(debounce)
	defer quiet.Stop()
	capT := time.NewTimer(maxLatency)
	capT.Stop()
	defer capT.Stop()
	var (
		dirty, ready = true, true // the initial snapshot needs no debounce
		pending      bool         // cap timer running for the current dirty period
		forced       bool         // cap fired: offer even if writes continue
		cached       bool         // snap holds a scan not yet sent
		stale        bool         // a change arrived after snap was scanned
		snap         Snapshot
		extra        []error
	)
	mark := func() {
		dirty = true
		if cached {
			stale = true
		}
		if !forced {
			cached, ready = false, false
			quiet.Reset(debounce)
		}
		if !pending {
			pending = true
			capT.Reset(maxLatency)
		}
	}
	for {
		var outc chan<- Snapshot
		if dirty && ready {
			if !cached {
				es, serrs := Scan(sessDir, procRoot)
				snap, extra = Snapshot{Entries: es, Errs: append(serrs, extra...)}, nil
				cached, stale = true, false
			}
			outc = out
		}
		select {
		case <-ctx.Done():
			return
		case outc <- snap:
			cached = false
			forced = false
			if stale { // changes landed while the consumer was slow
				stale, ready = false, false
				quiet.Reset(debounce)
				capT.Reset(maxLatency)
			} else {
				dirty, pending = false, false
			}
		case <-quiet.C:
			ready = true
		case <-capT.C:
			if pending {
				forced, ready = true, true
			}
		case err, ok := <-errs:
			if !ok {
				return
			}
			extra = append(extra, fmt.Errorf("registry watch: %w", err))
			mark()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if gone(ev) {
				select {
				case out <- Snapshot{Errs: []error{errDirGone}}:
				case <-ctx.Done():
				}
				return
			}
			if strings.HasSuffix(ev.Name, ".json") {
				mark()
			}
		}
	}
}
