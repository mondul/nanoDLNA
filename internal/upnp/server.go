package upnp

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nanodlna/internal/library"
	"nanodlna/internal/thumb"
	"nanodlna/internal/version"
)

// Config configures a Server.
type Config struct {
	// Name is the friendly name advertised to clients and used as the root
	// container title.
	Name string
	// RootPath is the absolute path of the folder being served.
	RootPath string
	// IP is the local address clients should connect to.
	IP net.IP
	// Port is the desired HTTP port. Zero lets the operating system choose.
	Port int
	// UDN overrides the auto-derived uuid: device name.
	UDN string
	// Logger receives all server diagnostics.
	Logger *slog.Logger
	// SubtitleCharset selects how subtitle files are transcoded on the wire.
	// One of "auto", "utf-8", "cp1252" or "latin1".
	SubtitleCharset string
	// DisableSSDP suppresses SSDP announcement and discovery.
	DisableSSDP bool
	// Thumbnails produces the picture shown beside each video. A nil maker, or
	// one without ffmpeg behind it, simply means no artwork is advertised.
	Thumbnails *thumb.Maker
}

// Server is a UPnP AV MediaServer exposing a library over HTTP and SSDP.
type Server struct {
	cfg Config
	lib *library.Library
	log *slog.Logger

	udn string

	mu      sync.RWMutex
	baseURL string
	port    int

	mux      *http.ServeMux
	httpSrv  *http.Server
	listener net.Listener
	errCh    chan error

	ssdp *ssdpService

	// icon is advertised in the device description.
	icon deviceIcon

	// GENA event subscriptions, keyed by SID.
	subsMu sync.Mutex
	subs   map[string]*subscription

	rescanMu sync.Mutex
	started  time.Time
}

// New creates a Server. Call Start to begin serving.
func New(cfg Config, lib *library.Library) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Name == "" {
		cfg.Name = version.Name
	}
	if cfg.SubtitleCharset == "" {
		cfg.SubtitleCharset = "auto"
	}
	if cfg.IP == nil {
		return nil, errors.New("upnp: a local IP address is required")
	}
	if cfg.RootPath == "" {
		return nil, errors.New("upnp: a media root path is required")
	}

	icon, err := loadIcon()
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:     cfg,
		lib:     lib,
		log:     cfg.Logger,
		errCh:   make(chan error, 1),
		subs:    map[string]*subscription{},
		icon:    icon,
		started: time.Now(),
	}
	if cfg.UDN != "" {
		s.udn = cfg.UDN
		if !strings.HasPrefix(s.udn, "uuid:") {
			s.udn = "uuid:" + s.udn
		}
	} else {
		s.udn = deriveUDN(cfg.RootPath)
	}

	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// Start binds the HTTP port and begins answering requests and SSDP searches.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(s.cfg.Port)))
	if err != nil {
		return fmt.Errorf("cannot listen on TCP port %d: %w", s.cfg.Port, err)
	}
	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port

	s.mu.Lock()
	s.baseURL = fmt.Sprintf("http://%s:%d", s.cfg.IP.String(), s.port)
	s.mu.Unlock()

	s.httpSrv = &http.Server{
		Handler:           s.withHeaders(s.mux),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}

	go func() {
		err := s.httpSrv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.errCh <- err:
			default:
			}
		}
	}()

	if !s.cfg.DisableSSDP {
		svc, err := newSSDPService(s)
		if err != nil {
			s.log.Warn("SSDP discovery is unavailable; clients can still connect by URL",
				"err", err)
		} else {
			s.ssdp = svc
			svc.start()
		}
	}
	return nil
}

// Errors reports a fatal HTTP server failure. It is closed when no error will
// be reported.
func (s *Server) Errors() <-chan error { return s.errCh }

// Close stops SSDP announcements and shuts the HTTP server down.
func (s *Server) Close() error {
	if s.ssdp != nil {
		s.ssdp.stop()
	}
	if s.httpSrv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}

// BaseURL returns the URL clients use to reach this server.
func (s *Server) BaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseURL
}

// UDN returns the device's unique identifier, including the "uuid:" prefix.
func (s *Server) UDN() string { return s.udn }

// Port returns the bound HTTP port.
func (s *Server) Port() int { return s.port }

// Rescan rebuilds the media index and tells listening clients that the content
// has changed.
func (s *Server) Rescan() (library.Stats, error) {
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()

	stats, err := s.lib.Scan()
	if err != nil {
		return stats, err
	}
	if s.ssdp != nil {
		s.ssdp.announceAlive()
	}
	s.notifySubscribers()
	return stats, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("/rootDesc.xml", s.handleRootDesc)
	s.mux.HandleFunc("/ContentDirectory/scpd.xml", staticXML(contentDirectorySCPD))
	s.mux.HandleFunc("/ConnectionManager/scpd.xml", staticXML(connectionManagerSCPD))
	s.mux.HandleFunc("/ContentDirectory/control", s.handleContentDirectory)
	s.mux.HandleFunc("/ConnectionManager/control", s.handleConnectionManager)
	s.mux.HandleFunc("/ContentDirectory/event", s.handleEvent)
	s.mux.HandleFunc("/ConnectionManager/event", s.handleEvent)
	s.mux.HandleFunc("/media/", s.handleMedia)
	s.mux.HandleFunc("/subs/", s.handleSubtitle)
	s.mux.HandleFunc("/thumb/", s.handleThumbnail)
	s.mux.HandleFunc(iconPath, s.handleIcon)
	s.mux.HandleFunc("/rescan", s.handleRescan)
	s.mux.HandleFunc("/", s.handleIndex)
}

func (s *Server) description() []byte {
	return []byte(fillDescription(deviceDescriptionXML, map[string]string{
		"name":    escapeXML(s.cfg.Name),
		"version": version.Version,
		"serial":  serialNumber(s.cfg.RootPath),
		"udn":     s.udn,
		"icon":    s.icon.xml(),
	}))
}

func (s *Server) handleRootDesc(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}
	body := s.description()
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func staticXML(body string) http.HandlerFunc {
	data := []byte(body)
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowGetHead(w, r) {
			return
		}
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(data)
	}
}

// allowGetHead rejects methods other than GET and HEAD, writing a 405 response.
func allowGetHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// withHeaders stamps the UPnP server identification header onto every response
// and logs requests at debug level.
func (s *Server) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", serverHeader())
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if s.log.Enabled(r.Context(), slog.LevelDebug) {
			s.log.Debug("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"remote", r.RemoteAddr,
				"took", time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Flush forwards to the underlying writer so that http.ServeContent can stream.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func serverHeader() string {
	return "Darwin/1.0 UPnP/1.0 " + version.UserAgent()
}

// deriveUDN builds a stable uuid: device name from the media root, so that
// televisions keep recognising the server across restarts.
func deriveUDN(seed string) string {
	sum := sha1.Sum([]byte("nanodlna:" + seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50 // RFC 4122 version 5
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return "uuid:" + formatUUID(b)
}

// serialNumber derives a short stable serial number from the media root.
func serialNumber(seed string) string {
	sum := sha1.Sum([]byte("nanodlna-serial:" + seed))
	return fmt.Sprintf("%02X%02X%02X%02X%02X%02X", sum[0], sum[1], sum[2], sum[3], sum[4], sum[5])
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// escapeXML escapes text for use in an XML document.
func escapeXML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
