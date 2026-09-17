// Package library builds an in-memory index of the video files in a directory
// tree and matches external subtitle files to their videos.
//
// The index is rebuilt wholesale on every Scan. Objects are identified by small
// integers so that the identifiers embedded in DLNA URLs and DIDL-Lite
// documents stay simple and widely compatible.
package library

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"nanodlna/internal/avmeta"
)

// Kind distinguishes containers from playable items.
type Kind uint8

const (
	// KindContainer is a browsable folder.
	KindContainer Kind = iota
	// KindVideo is a playable video item.
	KindVideo
)

// VideoExts maps a lower-case file extension to the MIME type advertised in
// DIDL-Lite and used as the HTTP Content-Type.
var VideoExts = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mkv":  "video/x-matroska",
	".webm": "video/webm",
	".avi":  "video/x-msvideo",
	".divx": "video/x-msvideo",
	".mov":  "video/quicktime",
	".mpg":  "video/mpeg",
	".mpeg": "video/mpeg",
	".m2v":  "video/mpeg",
	".ts":   "video/mp2t",
	".m2ts": "video/mp2t",
	".mts":  "video/mp2t",
	".wmv":  "video/x-ms-wmv",
	".asf":  "video/x-ms-asf",
	".flv":  "video/x-flv",
	".ogv":  "video/ogg",
	".ogg":  "video/ogg",
	".3gp":  "video/3gpp",
	".vob":  "video/mpeg",
}

// Subtitle is an external subtitle file matched to a video.
type Subtitle struct {
	Path   string // absolute path on disk
	Name   string // file name, used as the human readable track label
	Lang   string // lower-case language tag, empty when unknown
	Mime   string // MIME type used in the DLNA protocolInfo
	Forced bool
	SDH    bool
	Size   int64
}

// Label renders a short description of the subtitle for logs and the web UI.
func (s Subtitle) Label() string {
	parts := make([]string, 0, 3)
	if s.Lang != "" {
		parts = append(parts, strings.ToUpper(s.Lang))
	}
	if s.Forced {
		parts = append(parts, "forced")
	}
	if s.SDH {
		parts = append(parts, "SDH")
	}
	if len(parts) == 0 {
		return s.Name
	}
	return strings.Join(parts, ", ") + " \u2014 " + s.Name
}

// Node is a container or a video in the library tree.
type Node struct {
	ID       int
	ParentID int
	Kind     Kind
	Title    string
	Path     string
	ModTime  time.Time
	Parent   *Node
	Children []*Node

	// Video-only fields.
	Size int64
	Mime string
	Ext  string
	Info avmeta.Info
	Subs []Subtitle
}

// Options configures a Library.
type Options struct {
	// Name is the title given to the root container, usually the server name.
	Name string
	// SubLang is an ordered list of preferred subtitle language tags. Matched
	// languages are sorted first, in the order given here.
	SubLang []string
	// Workers bounds the concurrency used while probing container metadata.
	// Zero means runtime.NumCPU().
	Workers int
	// MaxDepth limits how deep the tree is walked. Zero means unlimited.
	MaxDepth int
	// Logger receives scan diagnostics. Nil means slog.Default().
	Logger *slog.Logger
}

// Stats summarises the most recent scan.
type Stats struct {
	Containers int
	Videos     int
	Subtitles  int
	Skipped    int
	Errors     int
	Elapsed    time.Duration
}

// Library is a concurrency-safe index of the media tree.
type Library struct {
	rootPath string
	opts     Options
	log      *slog.Logger

	mu       sync.RWMutex
	root     *Node
	byID     map[int]*Node
	stats    Stats
	updateID uint32
	scans    int
}

// New creates a Library rooted at rootPath. Call Scan to populate it.
func New(rootPath string, opts Options) *Library {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}
	// Copy before normalising so that the caller's slice is not modified.
	if len(opts.SubLang) > 0 {
		langs := make([]string, len(opts.SubLang))
		for i, l := range opts.SubLang {
			langs[i] = strings.ToLower(strings.TrimSpace(l))
		}
		opts.SubLang = langs
	}
	return &Library{
		rootPath: rootPath,
		opts:     opts,
		log:      opts.Logger,
		byID:     map[int]*Node{},
	}
}

// Root returns the current root container, or nil before the first Scan.
func (l *Library) Root() *Node {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.root
}

// Node returns the object with the given id, or nil.
func (l *Library) Node(id int) *Node {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byID[id]
}

// UpdateID returns the ContentDirectory SystemUpdateID, which changes whenever
// the index is rebuilt.
func (l *Library) UpdateID() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.updateID
}

// Stats returns a copy of the statistics from the most recent scan.
func (l *Library) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stats
}

// Videos returns every video in the tree in browse order.
func (l *Library) Videos() []*Node {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []*Node
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Kind == KindVideo {
			out = append(out, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(l.root)
	return out
}

// Scan rebuilds the index from the filesystem.
func (l *Library) Scan() (Stats, error) {
	started := time.Now()

	absRoot, err := filepath.Abs(l.rootPath)
	if err != nil {
		return Stats{}, fmt.Errorf("resolving %q: %w", l.rootPath, err)
	}
	absRoot = filepath.Clean(absRoot)

	info, err := os.Stat(absRoot)
	if err != nil {
		return Stats{}, fmt.Errorf("cannot access media folder: %w", err)
	}
	if !info.IsDir() {
		return Stats{}, fmt.Errorf("media path %s is not a directory", absRoot)
	}

	title := l.opts.Name
	if title == "" {
		title = filepath.Base(absRoot)
	}

	b := &builder{
		lib:     l,
		absRoot: absRoot,
		byID:    map[int]*Node{},
		nextID:  1,
	}
	b.root = &Node{
		ID:       0,
		ParentID: -1,
		Kind:     KindContainer,
		Title:    title,
		Path:     absRoot,
		ModTime:  info.ModTime(),
	}
	b.byID[0] = b.root

	b.walk(b.root, 0)

	// Videos are probed concurrently, so the tree must be complete and pruned
	// first: containers that end up holding no videos are dropped so that the
	// user does not see empty folders on the TV.
	b.prune(b.root)
	b.containers = countContainers(b.root)
	b.sortTree(b.root)
	b.probeAll()

	stats := Stats{
		Containers: b.containers,
		Videos:     b.videos,
		Subtitles:  b.subtitles,
		Skipped:    b.skipped,
		Errors:     b.errors,
		Elapsed:    time.Since(started),
	}

	l.mu.Lock()
	l.root = b.root
	l.byID = b.byID
	l.stats = stats
	l.scans++
	l.updateID = uint32(l.scans)
	l.mu.Unlock()

	return stats, nil
}

// builder accumulates state for one Scan.
type builder struct {
	lib     *Library
	absRoot string
	root    *Node
	byID    map[int]*Node
	nextID  int

	containers int
	videos     int
	subtitles  int
	skipped    int
	errors     int
}

func (b *builder) allocID(n *Node) int {
	id := b.nextID
	b.nextID++
	n.ID = id
	b.byID[id] = n
	return id
}

// skipNames are filesystem entries that are never useful to a media server.
var skipNames = map[string]bool{
	".ds_store":                 true,
	"thumbs.db":                 true,
	"desktop.ini":               true,
	"@eadir":                    true,
	"#recycle":                  true,
	"lost+found":                true,
	"$recycle.bin":              true,
	"system volume information": true,
	".trashes":                  true,
	".spotlight-v100":           true,
	".fseventsd":                true,
}

func (b *builder) walk(parent *Node, depth int) {
	if b.lib.opts.MaxDepth > 0 && depth >= b.lib.opts.MaxDepth {
		return
	}
	dir := parent.Path

	entries, err := os.ReadDir(dir)
	if err != nil {
		b.errors++
		b.lib.log.Warn("cannot read directory", "path", dir, "err", err)
		return
	}

	var dirs, vids []os.DirEntry
	var subPool []string

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipNames[strings.ToLower(name)] {
			b.skipped++
			continue
		}
		full := filepath.Join(dir, name)

		if e.IsDir() {
			if isSubtitleDirName(name) {
				// Subtitle folders are not browsable containers: their files
				// are folded into the parent's matching pool instead.
				subPool = append(subPool, listSubtitleFiles(full, 2)...)
				continue
			}
			dirs = append(dirs, e)
			continue
		}

		if !e.Type().IsRegular() {
			b.skipped++
			continue
		}

		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := SubExts[ext]; ok {
			subPool = append(subPool, full)
			continue
		}
		if _, ok := VideoExts[ext]; ok {
			vids = append(vids, e)
			continue
		}
		b.skipped++
	}

	sortEntries(dirs)
	sortEntries(vids)

	for _, e := range dirs {
		child := &Node{
			ParentID: parent.ID,
			Kind:     KindContainer,
			Title:    e.Name(),
			Path:     filepath.Join(dir, e.Name()),
			Parent:   parent,
		}
		if info, err := e.Info(); err == nil {
			child.ModTime = info.ModTime()
		}
		b.allocID(child)
		b.walk(child, depth+1)
		parent.Children = append(parent.Children, child)
	}

	for _, e := range vids {
		full := filepath.Join(dir, e.Name())
		ext := strings.ToLower(filepath.Ext(e.Name()))
		node := &Node{
			ParentID: parent.ID,
			Kind:     KindVideo,
			Title:    videoTitle(e.Name()),
			Path:     full,
			Parent:   parent,
			Ext:      ext,
			Mime:     VideoExts[ext],
		}
		if info, err := e.Info(); err == nil {
			node.Size = info.Size()
			node.ModTime = info.ModTime()
		} else {
			b.errors++
			b.lib.log.Warn("cannot stat video", "path", full, "err", err)
		}
		node.Subs = matchSubtitles(full, subPool, b.lib.opts.SubLang)
		b.subtitles += len(node.Subs)
		b.allocID(node)
		b.videos++
		parent.Children = append(parent.Children, node)
	}
}

// listSubtitleFiles returns subtitle files inside a subtitle-named directory,
// searching up to depth levels below it.
func listSubtitleFiles(dir string, depth int) []string {
	if depth <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			out = append(out, listSubtitleFiles(full, depth-1)...)
			continue
		}
		if _, ok := SubExts[strings.ToLower(filepath.Ext(name))]; ok {
			out = append(out, full)
		}
	}
	return out
}

// prune removes containers whose subtree contains no videos.
func (b *builder) prune(n *Node) bool {
	if n.Kind == KindVideo {
		return true
	}
	kept := n.Children[:0]
	for _, c := range n.Children {
		if b.prune(c) {
			kept = append(kept, c)
		}
	}
	n.Children = kept
	return len(n.Children) > 0 || n.Parent == nil
}

// sortTree sorts every container's children: folders first, then videos, by
// case-insensitive title.
func (b *builder) sortTree(n *Node) {
	if n.Kind != KindContainer {
		return
	}
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, c := n.Children[i], n.Children[j]
		if a.Kind != c.Kind {
			return a.Kind == KindContainer
		}
		return lessFold(a.Title, c.Title)
	})
	for _, c := range n.Children {
		b.sortTree(c)
	}
}

// probeAll fills in container metadata for every video using a worker pool.
func (b *builder) probeAll() {
	var nodes []*Node
	var collect func(*Node)
	collect = func(n *Node) {
		if n.Kind == KindVideo {
			nodes = append(nodes, n)
			return
		}
		for _, c := range n.Children {
			collect(c)
		}
	}
	collect(b.root)
	if len(nodes) == 0 {
		return
	}

	workers := b.lib.opts.Workers
	if workers > len(nodes) {
		workers = len(nodes)
	}

	jobs := make(chan *Node)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				n.Info = avmeta.ProbeFile(n.Path)
				if n.Info.Duration == 0 && n.Info.Width == 0 {
					b.lib.log.Debug("no container metadata", "path", n.Path)
				} else {
					b.lib.log.Debug("probed",
						"path", filepath.Base(n.Path),
						"duration", n.Info.Duration.Round(time.Second),
						"resolution", fmt.Sprintf("%dx%d", n.Info.Width, n.Info.Height))
				}
			}
		}()
	}
	for _, n := range nodes {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
}

// countContainers returns the number of containers in the pruned tree.
func countContainers(n *Node) int {
	if n.Kind != KindContainer {
		return 0
	}
	total := 1
	for _, c := range n.Children {
		total += countContainers(c)
	}
	return total
}

func sortEntries(entries []os.DirEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return lessFold(entries[i].Name(), entries[j].Name())
	})
}

// lessFold compares two strings case-insensitively, falling back to a
// case-sensitive comparison so that the order is total.
func lessFold(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// videoTitle derives the display title from a file name. Only underscores are
// expanded; dots are preserved because release names usually rely on them.
func videoTitle(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.ReplaceAll(name, "_", " ")
	return strings.Join(strings.Fields(name), " ")
}

// DisplayPath renders a node path relative to the media root for logs and the
// web UI.
func DisplayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	if rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}
