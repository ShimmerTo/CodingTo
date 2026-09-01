package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"codingto/internal/applog"
	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// gitWorkspaceEventDebounce coalesces ordinary file events: the worktree refresh
// runs only after writes stay quiet for this long.
const gitWorkspaceEventDebounce = 400 * time.Millisecond

// gitWorkspaceRefreshWindow keeps the full-refresh debounce for repository
// metadata changes (branches, remotes, history), which are expensive to rebuild.
const gitWorkspaceRefreshWindow = 10 * time.Second

const gitFileDetailCacheEntries = 64
const gitFileDetailCacheBytes int64 = 32 * 1024 * 1024
const gitFileDetailPrewarmLimit = 8
const gitFileDetailPrewarmWorkers = 2

type gitFileDetailCacheKey struct {
	scope      string
	path       string
	baseBranch string
	generation uint64
}

type gitFileDetailCacheValue struct {
	detail   GitFileDetail
	size     int64
	lastUsed uint64
}

type gitFileDetailFlight struct {
	done   chan struct{}
	detail GitFileDetail
	err    error
}

type gitFileDetailPrewarmItem struct {
	scope string
	path  string
}

// GitWorkspaceUpdate is emitted after a monitored workspace cache is refreshed.
type GitWorkspaceUpdate struct {
	Workspace    string          `json:"workspace"`
	Availability GitAvailability `json:"availability"`
}

type gitWorkspaceEntry struct {
	workspace          string
	metadataRoots      []string
	availability       GitAvailability
	repository         GitRepositoryView
	repositoryReady    bool
	refreshing         bool
	pending            bool
	pendingFull        bool
	timer              *time.Timer
	fullTimer          *time.Timer
	watcher            *fsnotify.Watcher
	stop               chan struct{}
	worktreeGeneration uint64
	metadataGeneration uint64
	fileDetails        map[gitFileDetailCacheKey]gitFileDetailCacheValue
	fileDetailFlights  map[gitFileDetailCacheKey]*gitFileDetailFlight
	fileDetailBytes    int64
	fileDetailClock    uint64
	prewarmRunning     bool
	prewarmPending     bool
	prewarmView        GitRepositoryView
}

type gitWorkspaceMonitor struct {
	mu      sync.Mutex
	entries map[string]*gitWorkspaceEntry
	closed  bool
	wg      sync.WaitGroup
}

func newGitWorkspaceMonitor() *gitWorkspaceMonitor {
	return &gitWorkspaceMonitor{entries: make(map[string]*gitWorkspaceEntry)}
}

func canonicalGitWorkspace(workspace string) (string, string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil {
		return "", "", err
	}
	absolute = filepath.Clean(absolute)
	if realPath, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
		absolute = filepath.Clean(realPath)
	}
	key := absolute
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, absolute, nil
}

// Ensure starts one cache and recursive file watcher per workspace. Repeated
// calls are idempotent and do not restart an existing watcher.
func (m *gitWorkspaceMonitor) Ensure(workspace string) {
	key, absolute, err := canonicalGitWorkspace(workspace)
	if err != nil || absolute == "" {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if _, exists := m.entries[key]; exists {
		m.mu.Unlock()
		return
	}
	entry := &gitWorkspaceEntry{
		workspace:         absolute,
		stop:              make(chan struct{}),
		fileDetails:       make(map[gitFileDetailCacheKey]gitFileDetailCacheValue),
		fileDetailFlights: make(map[gitFileDetailCacheKey]*gitFileDetailFlight),
	}
	m.entries[key] = entry
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(entry)
}

func (m *gitWorkspaceMonitor) run(entry *gitWorkspaceEntry) {
	defer m.wg.Done()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		applog.Errorf("initialize Git workspace watcher for %q: %v", entry.workspace, err)
		m.refresh(entry, true)
		return
	}
	entry.watcher = watcher
	defer watcher.Close()
	entry.metadataRoots = resolveGitMetadataRoots(entry.workspace)
	// The fsnotify Windows backend runs one I/O thread that blocks while its
	// events channel is full. The drain goroutine must run before any recursive
	// registration starts, otherwise a full channel stalls every pending Add and
	// the synchronous walk deadlocks (backend_windows.go AddWith <-in.reply).
	go m.watchEvents(entry, watcher)
	if err := addGitWorkspaceDirectories(watcher, entry.workspace); err != nil {
		applog.Warnf("watch Git workspace %q: %v", entry.workspace, err)
	}
	for _, metadataRoot := range entry.metadataRoots {
		if withinPath(entry.workspace, metadataRoot) {
			continue
		}
		if err := addGitMetadataDirectories(watcher, metadataRoot); err != nil {
			applog.Warnf("watch Git metadata %q for workspace %q: %v", metadataRoot, entry.workspace, err)
		}
	}
	m.refresh(entry, true)
	<-entry.stop
}

// watchEvents drains the watcher channels for the entry's lifetime. Create
// events on directories extend the recursive registration asynchronously so the
// drain never pauses while a subtree is being walked.
func (m *gitWorkspaceMonitor) watchEvents(entry *gitWorkspaceEntry, watcher *fsnotify.Watcher) {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			ignored := gitWorkspaceEventIgnored(entry.workspace, entry.metadataRoots, event.Name)
			if event.Op&fsnotify.Create != 0 && !ignored {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					go func(name string) {
						if addErr := addGitWorkspaceDirectories(watcher, name); addErr != nil {
							applog.Warnf("extend Git workspace watcher %q: %v", name, addErr)
						}
					}(event.Name)
				}
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) != 0 {
				if ignored {
					continue
				}
				m.schedule(entry, gitMetadataNeedsFullRefresh(entry.workspace, entry.metadataRoots, event.Name))
			}
		case watchErr, ok := <-watcher.Errors:
			if ok {
				applog.Warnf("Git workspace watcher %q: %v", entry.workspace, watchErr)
			}
		}
	}
}

// gitMetadataNeedsFullRefresh reports whether a watched event changes refs,
// repository state, history or configuration. Index-only changes need just the
// worktree snapshot; every other metadata change rebuilds the complete model.
func gitMetadataNeedsFullRefresh(workspace string, metadataRoots []string, changedPath string) bool {
	relative, metadata := gitMetadataRelativePath(workspace, metadataRoots, changedPath)
	if !metadata {
		return false
	}
	return relative != "index" && relative != "index.lock"
}

// gitWorkspaceEventIgnored reports whether a watched event can never change the
// user-visible model. Object, log and hook churn under Git metadata is
// high-frequency, so it must not restart the refresh debounce.
func gitWorkspaceEventIgnored(workspace string, metadataRoots []string, changedPath string) bool {
	relative, metadata := gitMetadataRelativePath(workspace, metadataRoots, changedPath)
	if !metadata {
		return false
	}
	return relative == "objects" || strings.HasPrefix(relative, "objects/") ||
		relative == "logs" || strings.HasPrefix(relative, "logs/") ||
		relative == "hooks" || strings.HasPrefix(relative, "hooks/")
}

func gitMetadataRelativePath(workspace string, metadataRoots []string, changedPath string) (string, bool) {
	for _, root := range metadataRoots {
		if !withinPath(root, changedPath) {
			continue
		}
		relative, err := filepath.Rel(root, changedPath)
		if err == nil {
			return filepath.ToSlash(relative), true
		}
	}
	relative, err := filepath.Rel(workspace, changedPath)
	if err != nil {
		return "", false
	}
	slash := filepath.ToSlash(relative)
	if slash == ".git" {
		return ".", true
	}
	if strings.HasPrefix(slash, ".git/") {
		return strings.TrimPrefix(slash, ".git/"), true
	}
	return "", false
}

func resolveGitMetadataRoots(workspace string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	values := []string{
		gitTrimmed(ctx, workspace, "rev-parse", "--absolute-git-dir"),
		gitTrimmed(ctx, workspace, "rev-parse", "--git-common-dir"),
	}
	roots := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(workspace, value)
		}
		value = filepath.Clean(value)
		key := value
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, value)
	}
	return roots
}

// addGitWorkspaceDirectories registers root and every nested directory with the
// watcher so file events inside any subdirectory are observable. Object and log
// storage under .git is skipped because it churns on every Git operation without
// changing the user-visible model. One inaccessible or transiently removed
// subdirectory must not stop the remaining recursive registration.
func addGitWorkspaceDirectories(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if item != nil && item.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err == nil {
			slash := filepath.ToSlash(relative)
			if slash == ".git/objects" || strings.HasPrefix(slash, ".git/objects/") || slash == ".git/logs" || strings.HasPrefix(slash, ".git/logs/") {
				return fs.SkipDir
			}
		}
		if err := watcher.Add(path); err != nil {
			if path == root && !os.IsNotExist(err) && !os.IsPermission(err) {
				return err
			}
			return nil
		}
		return nil
	})
}

func addGitMetadataDirectories(watcher *fsnotify.Watcher, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if err := watcher.Add(root); err != nil && !os.IsNotExist(err) && !os.IsPermission(err) {
		return err
	}
	refs := filepath.Join(root, "refs")
	if _, err := os.Stat(refs); err == nil {
		return addGitWorkspaceDirectories(watcher, refs)
	} else if !os.IsNotExist(err) && !os.IsPermission(err) {
		return err
	}
	return nil
}

func (m *gitWorkspaceMonitor) schedule(entry *gitWorkspaceEntry, full bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	entry.worktreeGeneration++
	if full {
		entry.metadataGeneration++
	}
	m.clearMutableFileDetailsLocked(entry)
	if full {
		entry.pendingFull = true
		if entry.fullTimer != nil {
			return
		}
		var fullTimer *time.Timer
		fullTimer = time.AfterFunc(gitWorkspaceRefreshWindow, func() {
			m.mu.Lock()
			if entry.fullTimer == fullTimer {
				entry.fullTimer = nil
				entry.pendingFull = false
			}
			m.mu.Unlock()
			m.refresh(entry, true)
		})
		entry.fullTimer = fullTimer
		return
	}
	// True debounce: every ordinary file event restarts the short window, so a
	// write burst settles before the worktree refresh runs.
	if entry.timer != nil {
		entry.timer.Stop()
	}
	var debounceTimer *time.Timer
	debounceTimer = time.AfterFunc(gitWorkspaceEventDebounce, func() {
		m.mu.Lock()
		if entry.timer == debounceTimer {
			entry.timer = nil
		}
		m.mu.Unlock()
		m.refresh(entry, false)
	})
	entry.timer = debounceTimer
}

// refresh rebuilds the cached model for one entry. It returns false when
// another refresh cycle is already running, in which case the request is folded
// into the pending flag and the running cycle re-runs afterwards.
func (m *gitWorkspaceMonitor) refresh(entry *gitWorkspaceEntry, full bool) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	if entry.refreshing {
		entry.pending = true
		entry.pendingFull = entry.pendingFull || full
		m.mu.Unlock()
		return false
	}
	entry.refreshing = true
	m.mu.Unlock()

	if full {
		view := readGitRepositoryView(entry.workspace)
		availability := gitAvailabilityFromRepository(view)
		m.mu.Lock()
		entry.repository = view
		entry.repositoryReady = true
		entry.availability = availability
		entry.worktreeGeneration++
		entry.metadataGeneration++
		m.clearMutableFileDetailsLocked(entry)
		m.mu.Unlock()
		m.emit(entry.workspace, availability)
		m.scheduleFileDetailPrewarm(entry, view)
	} else {
		snapshot, ok := readGitWorktreeSnapshot(entry.workspace)
		availability := gitAvailabilityFromSnapshot(snapshot)
		var updatedView GitRepositoryView
		viewReady := false
		m.mu.Lock()
		promoteToFull := entry.repositoryReady && !entry.repository.IsRepository && snapshot.IsRepository
		if entry.repositoryReady {
			if ok {
				// Ahead/Behind derive from refs, which ordinary file events never
				// touch; keep the last metadata-derived values until a full refresh.
				availability.Ahead = entry.repository.Ahead
				entry.repository = gitRepositoryWithWorktreeSnapshot(entry.repository, snapshot)
				entry.availability = availability
				entry.worktreeGeneration++
				m.clearMutableFileDetailsLocked(entry)
				updatedView = entry.repository
				viewReady = true
			}
			// A failed status read must not overwrite the cached worktree with an
			// empty one; the next event or explicit refresh will retry.
		} else if ok {
			entry.availability = availability
		}
		m.mu.Unlock()
		if ok {
			m.emit(entry.workspace, availability)
			if viewReady {
				m.scheduleFileDetailPrewarm(entry, updatedView)
			}
		}
		if promoteToFull {
			m.mu.Lock()
			entry.pending = true
			entry.pendingFull = true
			m.mu.Unlock()
		}
	}

	m.mu.Lock()
	entry.refreshing = false
	pending := entry.pending
	pendingFull := entry.pendingFull
	entry.pending = false
	entry.pendingFull = false
	m.mu.Unlock()
	if pending {
		go m.refresh(entry, pendingFull)
	}
	return true
}

func (m *gitWorkspaceMonitor) RefreshNow(workspace string, full bool) {
	key, _, err := canonicalGitWorkspace(workspace)
	if err != nil {
		return
	}
	m.Ensure(workspace)
	m.mu.Lock()
	entry := m.entries[key]
	if entry != nil {
		entry.worktreeGeneration++
		if full {
			entry.metadataGeneration++
		}
		m.clearMutableFileDetailsLocked(entry)
	}
	m.mu.Unlock()
	if entry != nil {
		go m.refresh(entry, full)
	}
}

func (m *gitWorkspaceMonitor) Availability(workspace string) (GitAvailability, bool) {
	key, _, err := canonicalGitWorkspace(workspace)
	if err != nil {
		return GitAvailability{}, false
	}
	m.Ensure(workspace)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[key]
	if entry == nil || (!entry.repositoryReady && entry.availability.Root == "" && !entry.availability.IsRepository) {
		return GitAvailability{}, false
	}
	return entry.availability, true
}

func (m *gitWorkspaceMonitor) Repository(workspace string) (GitRepositoryView, bool) {
	key, _, err := canonicalGitWorkspace(workspace)
	if err != nil {
		return GitRepositoryView{}, false
	}
	m.Ensure(workspace)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[key]
	if entry == nil || !entry.repositoryReady {
		return GitRepositoryView{}, false
	}
	return entry.repository, true
}

func (m *gitWorkspaceMonitor) StoreRepository(workspace string, view GitRepositoryView) {
	key, _, err := canonicalGitWorkspace(workspace)
	if err != nil {
		return
	}
	m.Ensure(workspace)
	m.mu.Lock()
	if entry := m.entries[key]; entry != nil {
		entry.repository = view
		entry.repositoryReady = true
		entry.availability = gitAvailabilityFromRepository(view)
		entry.worktreeGeneration++
		entry.metadataGeneration++
		m.clearMutableFileDetailsLocked(entry)
	}
	m.mu.Unlock()
}

// FileDetail returns a cached file comparison or performs one shared bounded
// computation against the current cached repository model.
func (m *gitWorkspaceMonitor) FileDetail(workspace, scope, path, baseBranch string) (GitFileDetail, error, bool) {
	scope = strings.TrimSpace(scope)
	baseBranch = strings.TrimSpace(baseBranch)
	return m.fileDetailCached(
		workspace, scope, path, baseBranch,
		func(entry *gitWorkspaceEntry) uint64 {
			if scope == "branch" {
				return entry.metadataGeneration
			}
			return entry.worktreeGeneration
		},
		func(view GitRepositoryView, normalizedPath string) (GitFileDetail, error) {
			return readGitFileDetailFromRepository(view, scope, normalizedPath, baseBranch)
		},
	)
}

// CommitFileDetail caches one immutable commit comparison by commit hash and path.
func (m *gitWorkspaceMonitor) CommitFileDetail(workspace, commit, path string, load func(string, string) (GitFileDetail, error)) (GitFileDetail, error, bool) {
	commit = strings.TrimSpace(commit)
	return m.fileDetailCached(
		workspace, "commit", path, commit,
		func(*gitWorkspaceEntry) uint64 { return 0 },
		func(view GitRepositoryView, normalizedPath string) (GitFileDetail, error) {
			return load(view.Root, normalizedPath)
		},
	)
}

func (m *gitWorkspaceMonitor) fileDetailCached(
	workspace, scope, path, baseBranch string,
	generationFor func(*gitWorkspaceEntry) uint64,
	load func(GitRepositoryView, string) (GitFileDetail, error),
) (GitFileDetail, error, bool) {
	key, _, err := canonicalGitWorkspace(workspace)
	if err != nil {
		return GitFileDetail{}, err, false
	}
	m.Ensure(workspace)
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if path == "" || path == "." {
		return GitFileDetail{}, errors.New("invalid Git file path"), true
	}

	m.mu.Lock()
	entry := m.entries[key]
	if m.closed || entry == nil || !entry.repositoryReady {
		m.mu.Unlock()
		return GitFileDetail{}, nil, false
	}
	generation := generationFor(entry)
	cacheKey := gitFileDetailCacheKey{scope: scope, path: path, baseBranch: baseBranch, generation: generation}
	if cached, ok := entry.fileDetails[cacheKey]; ok {
		entry.fileDetailClock++
		cached.lastUsed = entry.fileDetailClock
		entry.fileDetails[cacheKey] = cached
		m.mu.Unlock()
		return cached.detail, nil, true
	}
	if flight := entry.fileDetailFlights[cacheKey]; flight != nil {
		done := flight.done
		m.mu.Unlock()
		<-done
		return flight.detail, flight.err, true
	}
	flight := &gitFileDetailFlight{done: make(chan struct{})}
	entry.fileDetailFlights[cacheKey] = flight
	view := entry.repository
	m.mu.Unlock()

	detail, loadErr := load(view, path)
	size := gitFileDetailSize(detail)
	m.mu.Lock()
	flight.detail = detail
	flight.err = loadErr
	delete(entry.fileDetailFlights, cacheKey)
	currentGeneration := generationFor(entry)
	if loadErr == nil && !m.closed && entry.repositoryReady && currentGeneration == generation && size <= gitFileDetailCacheBytes {
		entry.fileDetailClock++
		entry.fileDetails[cacheKey] = gitFileDetailCacheValue{detail: detail, size: size, lastUsed: entry.fileDetailClock}
		entry.fileDetailBytes += size
		m.trimFileDetailsLocked(entry)
	}
	close(flight.done)
	m.mu.Unlock()
	return detail, loadErr, true
}

func (m *gitWorkspaceMonitor) clearMutableFileDetailsLocked(entry *gitWorkspaceEntry) {
	for key, value := range entry.fileDetails {
		if key.scope == "commit" {
			continue
		}
		delete(entry.fileDetails, key)
		entry.fileDetailBytes -= value.size
	}
}

func (m *gitWorkspaceMonitor) trimFileDetailsLocked(entry *gitWorkspaceEntry) {
	for len(entry.fileDetails) > gitFileDetailCacheEntries || entry.fileDetailBytes > gitFileDetailCacheBytes {
		var oldestKey gitFileDetailCacheKey
		var oldest gitFileDetailCacheValue
		found := false
		for key, value := range entry.fileDetails {
			if !found || value.lastUsed < oldest.lastUsed {
				oldestKey, oldest, found = key, value, true
			}
		}
		if !found {
			return
		}
		delete(entry.fileDetails, oldestKey)
		entry.fileDetailBytes -= oldest.size
	}
}

func gitFileDetailSize(detail GitFileDetail) int64 {
	size := int64(len(detail.Before.Text) + len(detail.After.Text) + len(detail.Before.ImageData) + len(detail.After.ImageData))
	for _, hunk := range detail.Hunks {
		size += int64(len(hunk.Header))
		for _, line := range hunk.Lines {
			size += int64(len(line.Text) + 24)
		}
	}
	return size + 512
}

func (m *gitWorkspaceMonitor) scheduleFileDetailPrewarm(entry *gitWorkspaceEntry, view GitRepositoryView) {
	if !view.IsRepository || len(view.Worktree.Files) == 0 {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	entry.prewarmView = view
	entry.prewarmPending = true
	if entry.prewarmRunning {
		m.mu.Unlock()
		return
	}
	entry.prewarmRunning = true
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runFileDetailPrewarm(entry)
}

func (m *gitWorkspaceMonitor) runFileDetailPrewarm(entry *gitWorkspaceEntry) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		if m.closed {
			entry.prewarmRunning = false
			m.mu.Unlock()
			return
		}
		view := entry.prewarmView
		generation := entry.worktreeGeneration
		entry.prewarmPending = false
		m.mu.Unlock()

		items := gitFileDetailPrewarmItems(view.Worktree.Files)
		jobs := make(chan gitFileDetailPrewarmItem)
		var workers sync.WaitGroup
		for index := 0; index < gitFileDetailPrewarmWorkers; index++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for item := range jobs {
					_, _, _ = m.FileDetail(entry.workspace, item.scope, item.path, "")
				}
			}()
		}
		for _, item := range items {
			m.mu.Lock()
			current := !m.closed && entry.worktreeGeneration == generation
			m.mu.Unlock()
			if !current {
				break
			}
			jobs <- item
		}
		close(jobs)
		workers.Wait()

		m.mu.Lock()
		if !entry.prewarmPending || m.closed {
			entry.prewarmRunning = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func gitFileDetailPrewarmItems(files []GitFileChange) []gitFileDetailPrewarmItem {
	items := make([]gitFileDetailPrewarmItem, 0, gitFileDetailPrewarmLimit)
	appendScope := func(scope string, matches func(GitFileChange) bool) {
		for _, file := range files {
			if len(items) >= gitFileDetailPrewarmLimit {
				return
			}
			if file.Conflicted || !matches(file) {
				continue
			}
			items = append(items, gitFileDetailPrewarmItem{scope: scope, path: file.Path})
		}
	}
	appendScope("staged", func(file GitFileChange) bool { return file.Staged })
	appendScope("unstaged", func(file GitFileChange) bool { return file.Unstaged && !file.Untracked })
	appendScope("untracked", func(file GitFileChange) bool { return file.Untracked })
	return items
}

func (m *gitWorkspaceMonitor) emit(workspace string, availability GitAvailability) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit("git:workspace", GitWorkspaceUpdate{Workspace: workspace, Availability: availability})
}

func (m *gitWorkspaceMonitor) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, entry := range m.entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		if entry.fullTimer != nil {
			entry.fullTimer.Stop()
		}
		close(entry.stop)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (a *App) prewarmRecentGitWorkspaces(limit int) {
	if a.gitMonitor == nil {
		return
	}
	a.gitMonitor.Ensure(a.defaultWorkDir)
	if limit <= 0 {
		return
	}
	items, err := a.store.Store().ListSessions()
	if err != nil {
		applog.Warnf("prewarm Git workspaces: list conversations: %v", err)
		return
	}
	cfg := a.store.Get()
	seen := make(map[string]struct{}, limit)
	for _, item := range items {
		workspace := a.sessionWorkspace(item.EnvironmentID, "")
		if strings.TrimSpace(workspace) == "" && strings.TrimSpace(item.SessionDir) != "" {
			if changes, changeErr := readSessionChanges(item.SessionDir); changeErr == nil {
				workspace = strings.TrimSpace(changes.Root)
			}
		}
		if workspace == "" {
			if environment := cfg.environmentByID(item.EnvironmentID); environment != nil {
				workspace = strings.TrimSpace(environment.Path)
			}
		}
		key, absolute, keyErr := canonicalGitWorkspace(workspace)
		if keyErr != nil || absolute == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		a.gitMonitor.Ensure(absolute)
		if len(seen) >= limit {
			return
		}
	}
}

func gitConflictedFiles(files []GitFileChange) []GitFileChange {
	conflicts := make([]GitFileChange, 0)
	for _, file := range files {
		if file.Conflicted {
			conflicts = append(conflicts, file)
		}
	}
	return conflicts
}

func gitRepositoryWithWorktreeSnapshot(repository GitRepositoryView, snapshot GitSnapshot) GitRepositoryView {
	repository.IsRepository = snapshot.IsRepository
	repository.Root = snapshot.Root
	repository.WorktreePath = snapshot.WorktreePath
	repository.CurrentBranch = snapshot.CurrentBranch
	repository.Worktree = snapshot.Worktree
	repository.Conflicts = gitConflictedFiles(snapshot.Worktree.Files)
	repository.HasConflicts = len(repository.Conflicts) > 0
	return repository
}

func gitAvailabilityFromSnapshot(snapshot GitSnapshot) GitAvailability {
	conflicts := gitConflictedFiles(snapshot.Worktree.Files)
	return GitAvailability{
		IsRepository: snapshot.IsRepository, Root: snapshot.Root,
		CurrentBranch: snapshot.CurrentBranch, ChangeCount: len(snapshot.Worktree.Files),
		Ahead: snapshot.Ahead, HasConflicts: len(conflicts) > 0,
	}
}

func gitAvailabilityFromRepository(view GitRepositoryView) GitAvailability {
	return GitAvailability{
		IsRepository: view.IsRepository, Root: view.Root, CurrentBranch: view.CurrentBranch,
		ChangeCount: len(view.Worktree.Files), Ahead: view.Ahead, HasConflicts: view.HasConflicts,
	}
}
