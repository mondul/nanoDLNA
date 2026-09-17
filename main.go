// Command nanoDLNA serves a folder of videos over DLNA/UPnP so that a
// television can browse and play them, together with their external subtitle
// files.
//
// Run it inside the folder that holds the films:
//
//	nanoDLNA
//
// or name the folder, which is also what dragging one onto the program does:
//
//	nanoDLNA /path/to/films
//
// The library is published under the friendly name nanoDLNA. Use -name to change
// the name shown on the television and -h for the full list of options.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"nanodlna/internal/cache"
	"nanodlna/internal/library"
	"nanodlna/internal/probe"
	"nanodlna/internal/thumb"
	"nanodlna/internal/tools"
	"nanodlna/internal/upnp"
	"nanodlna/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.Name, err)
		os.Exit(1)
	}
}

type options struct {
	dir          string
	name         string
	port         int
	iface        string
	subLang      string
	charset      string
	cache        string
	validate     string
	probeTimeout time.Duration
	revalidate   bool
	thumbnails   string
	thumbSize    int
	thumbPos     int
	logLevel     string
	noSSDP       bool
	showVer      bool
}

func run(args []string) error {
	opts, portSet, err := parseFlags(args)
	if err != nil {
		return err
	}
	if opts.showVer {
		fmt.Printf("%s %s\n", version.Name, version.Version)
		return nil
	}

	logger, err := newLogger(opts.logLevel)
	if err != nil {
		return err
	}

	// The external tools are optional, but knowing which are missing at start-up
	// explains later why a feature is quiet rather than broken.
	external := tools.Detect(context.Background())
	logger.Debug("external tools",
		"ffprobe", describeTool(external.FFprobe),
		"ffmpeg", describeTool(external.FFmpeg))

	root, err := filepath.Abs(opts.dir)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", opts.dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("cannot open media folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", root)
	}

	ip, source, err := resolveLocalIP(opts.iface)
	if err != nil {
		return err
	}
	logger.Debug("using local address", "ip", ip.String(), "source", source)

	name := deviceName(opts.name, logger)

	level, err := probe.ParseLevel(opts.validate)
	if err != nil {
		return err
	}

	cacheDir := opts.cache
	if cacheDir == "" {
		cacheDir, err = cache.Dir(root)
		if err != nil {
			logger.Warn("cannot locate a cache directory, so results will not be remembered", "err", err)
			cacheDir = ""
		} else {
			logger.Debug("using cache directory", "path", cacheDir)
		}
	}

	var store *probe.Store
	if cacheDir != "" {
		store = probe.OpenStore(filepath.Join(cacheDir, "probe.json"), logger)
	}

	prober := probe.New(probe.Config{
		Level:      level,
		Probe:      external.FFprobe,
		Decode:     external.FFmpeg,
		Store:      store,
		Timeout:    opts.probeTimeout,
		Revalidate: opts.revalidate,
		Logger:     logger,
	})

	// probe and library know nothing about each other, so the translation from
	// one package's verdict to the other's lives here.
	var validate library.Validator
	if prober.Level() != probe.Off {
		validate = func(path string, size int64, mod time.Time) library.Verdict {
			v := prober.Check(path, size, mod)
			return library.Verdict{
				Serve:    v.Serve,
				Reason:   v.Reason,
				Duration: v.Duration,
				Width:    v.Width,
				Height:   v.Height,
			}
		}
	}

	lib := library.New(root, library.Options{
		Name:     name,
		SubLang:  splitList(opts.subLang),
		Workers:  runtime.NumCPU(),
		Logger:   logger,
		Validate: validate,
		// Saved here rather than after the first scan only, because the web
		// page's rescan goes through the same path.
		AfterScan: func() {
			if err := prober.Save(); err != nil {
				logger.Warn("cannot save the validation cache", "err", err)
			}
		},
	})

	logger.Info("scanning media folder", "path", root)
	stats, err := lib.Scan()
	if err != nil {
		return err
	}
	logger.Info("scan complete",
		"videos", stats.Videos,
		"folders", stats.Containers,
		"subtitles", stats.Subtitles,
		"held_back", stats.Rejected,
		"took", stats.Elapsed.Round(time.Millisecond))

	if stats.Rejected > 0 {
		logger.Warn("some files could not be read and will not be served",
			"count", stats.Rejected, "see", "the web page")
	}

	thumbs := thumb.New(thumb.Config{
		Tool:   external.FFmpeg,
		Dir:    cacheDir,
		Size:   opts.thumbSize,
		Pos:    opts.thumbPos,
		Logger: logger,
	})
	if thumbnailsWanted(opts.thumbnails) && !thumbs.Available() {
		logger.Warn("thumbnails are on but ffmpeg was not found, so no artwork will be offered")
	}
	if !thumbnailsWanted(opts.thumbnails) {
		thumbs = thumb.New(thumb.Config{Logger: logger})
	}
	if thumbs.Available() && cacheDir == "" {
		logger.Warn("thumbnails need somewhere to cache their output; none is available")
		thumbs = thumb.New(thumb.Config{Logger: logger})
	}

	srv, err := upnp.New(upnp.Config{
		Name:            name,
		RootPath:        root,
		IP:              ip,
		Port:            opts.port,
		Logger:          logger,
		SubtitleCharset: opts.charset,
		DisableSSDP:     opts.noSSDP,
		Thumbnails:      thumbs,
	}, lib)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		// A busy default port should not stop the user from starting the
		// server, but an explicitly requested port is honoured strictly.
		if portSet || !isAddrInUse(err) {
			return err
		}
		logger.Warn("the default port is busy; letting the system choose another one",
			"port", opts.port)
		fallback, ferr := upnp.New(upnp.Config{
			Name:            name,
			RootPath:        root,
			IP:              ip,
			Port:            0,
			Logger:          logger,
			SubtitleCharset: opts.charset,
			DisableSSDP:     opts.noSSDP,
			Thumbnails:      thumbs,
		}, lib)
		if ferr != nil {
			return err
		}
		srv = fallback
		if err := srv.Start(ctx); err != nil {
			return err
		}
	}

	printBanner(srv, stats, bannerInfo{
		Name:       name,
		Root:       root,
		NoSSDP:     opts.noSSDP,
		Validation: validationSummary(prober.Level(), external.FFprobe),
		Thumbnails: thumbnailSummary(thumbs, opts.thumbSize, opts.thumbPos),
		CacheDir:   cacheDir,
	}, logger)

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		logger.Info("shutting down")
	case err := <-srv.Errors():
		if err != nil {
			return fmt.Errorf("http server stopped: %w", err)
		}
	}
	return srv.Close()
}

func parseFlags(args []string) (options, bool, error) {
	// With no folder argument the folder the program was started in is served.
	opts := options{dir: "."}

	fs := flag.NewFlagSet(version.Name, flag.ExitOnError)
	fs.StringVar(&opts.name, "name", "", `name shown on the television (default "nanoDLNA [host name]")`)
	fs.IntVar(&opts.port, "port", 8200, "HTTP port to listen on; 0 lets the system choose")
	fs.StringVar(&opts.iface, "iface", "", "network interface name or local IP address to advertise (default: auto)")
	fs.StringVar(&opts.subLang, "sub-lang", "", "preferred subtitle languages, most preferred first, for example \"it,en\"")
	fs.StringVar(&opts.charset, "charset", "auto", "subtitle text encoding: auto, utf-8, cp1252 or latin1")
	fs.StringVar(&opts.validate, "validate", "container", "check files with ffprobe before serving them: off, container or content")
	fs.StringVar(&opts.cache, "cache", "", "where to keep validation results and thumbnails (default: ~/.nanoDLNA/cache)")
	fs.DurationVar(&opts.probeTimeout, "validate-timeout", 20*time.Second, "how long a single ffprobe or ffmpeg run may take")
	fs.BoolVar(&opts.revalidate, "revalidate", false, "check every file again, ignoring cached results")
	fs.StringVar(&opts.thumbnails, "thumbnails", "on", "generate artwork for videos with ffmpeg: on or off")
	fs.IntVar(&opts.thumbSize, "thumb-size", 256, "edge length of the square thumbnails")
	fs.IntVar(&opts.thumbPos, "thumb-pos", 25, "where in a video to take the thumbnail, as a percentage of its duration")
	fs.StringVar(&opts.logLevel, "log", "info", "log level: debug, info, warn or error")
	fs.BoolVar(&opts.noSSDP, "no-ssdp", false, "do not advertise the server with SSDP discovery")
	fs.BoolVar(&opts.showVer, "version", false, "print the version and exit")

	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "%s %s - DLNA media server for a folder of videos\n\n", version.Name, version.Version)
		fmt.Fprintf(out, "Usage:\n  %s [options] [folder]\n\n", version.Name)
		fmt.Fprintf(out, "Every .mp4 or .mkv in the folder is published, together with any matching\n")
		fmt.Fprintf(out, ".srt subtitle file. With no folder the one you are standing in is served,\n")
		fmt.Fprintf(out, "and dragging a folder onto the program passes it as this argument.\n\nOptions:\n")
		fs.PrintDefaults()
		fmt.Fprintf(out, `
Subtitle matching:
  A subtitle is attached when its name starts with the video's name, so
  "Movie.mkv" picks up "Movie.srt", "Movie.en.srt", "Movie.en.forced.srt" or
  "Movie.mkv.srt". Subtitles inside a "Subs" or "Subtitles" subfolder are
  matched the same way. Subtitle files are streamed as UTF-8 whatever their
  original encoding.

Troubleshooting:
  If the television does not list the server, allow incoming connections for
  %s in the firewall, make sure the television is on the same network, and
  check that the address printed above is reachable from it.
`, version.Name)
	}

	if err := fs.Parse(args); err != nil {
		return options{}, false, err
	}

	switch fs.NArg() {
	case 0:
		// Serve the current folder.
	case 1:
		opts.dir = fs.Arg(0)
	default:
		// flag stops parsing at the first non-flag argument, so options placed
		// after the folder land here as extra arguments.
		return options{}, false, fmt.Errorf(
			"only one folder can be served and options must come before it; got: %s",
			strings.Join(fs.Args(), " "))
	}

	portSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			portSet = true
		}
	})
	return opts, portSet, nil
}

func newLogger(level string) (*slog.Logger, error) {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "", "info":
		lv = slog.LevelInfo
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown log level %q (use debug, info, warn or error)", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})), nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// resolveLocalIP picks the address that clients should connect to.
func resolveLocalIP(ifaceFlag string) (net.IP, string, error) {
	if ifaceFlag != "" {
		if ip := net.ParseIP(ifaceFlag); ip != nil {
			ip4 := ip.To4()
			if ip4 == nil {
				return nil, "", fmt.Errorf("%s is not an IPv4 address (nanoDLNA serves IPv4 only)", ifaceFlag)
			}
			return ip4, "command line", nil
		}
		ifi, err := net.InterfaceByName(ifaceFlag)
		if err != nil {
			return nil, "", fmt.Errorf("no network interface named %q", ifaceFlag)
		}
		ip, err := firstIPv4(ifi)
		if err != nil {
			return nil, "", err
		}
		return ip, "interface " + ifi.Name, nil
	}

	// Asking the kernel which source address it would use for an outbound
	// connection is the most reliable way to find the LAN address.
	if conn, err := net.Dial("udp4", "8.8.8.8:80"); err == nil {
		addr, ok := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if ok {
			if ip4 := addr.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4, "default route", nil
			}
		}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, "", fmt.Errorf("listing network interfaces: %w", err)
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip, err := firstIPv4(ifi); err == nil {
			return ip, "interface " + ifi.Name, nil
		}
	}
	return nil, "", errors.New("could not find a local IPv4 address; pass -iface <name-or-ip>")
}

func firstIPv4(ifi *net.Interface) (net.IP, error) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, fmt.Errorf("reading addresses of %s: %w", ifi.Name, err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
			return ip4, nil
		}
	}
	return nil, fmt.Errorf("interface %s has no IPv4 address", ifi.Name)
}

// isAddrInUse reports whether err is a "port already in use" failure.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use")
}

// hostname reads the machine's host name. It is a variable so that a test can
// make it fail without needing a machine whose host name is broken.
var hostname = os.Hostname

// thumbnailsWanted reads the -thumbnails flag. Anything that is not clearly
// "off" leaves them on, so a typo does not silently disable a feature.
func thumbnailsWanted(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "no", "0", "none":
		return false
	default:
		return true
	}
}

// validationSummary describes what checking is in force.
func validationSummary(level probe.Level, ffprobe *tools.Tool) string {
	if level == probe.Off {
		if ffprobe == nil {
			return "off (ffprobe not found)"
		}
		return "off"
	}
	return fmt.Sprintf("%s (%s)", level, ffprobe.Short())
}

// thumbnailSummary describes the artwork that will be offered.
func thumbnailSummary(maker *thumb.Maker, size, pos int) string {
	if !maker.Available() {
		return "off (ffmpeg not found)"
	}
	return fmt.Sprintf("%dx%d at %d%% of the video", size, size, pos)
}

// describeTool renders a detected program for a log line.
func describeTool(t *tools.Tool) string {
	if t == nil {
		return "not found"
	}
	if t.Version == "" {
		return t.Path
	}
	return t.Version
}

// deviceName returns the name advertised to players.
//
// Without -name it is "nanoDLNA [host]", which is what tells two servers apart
// in a player's list when more than one is running on a network. A host name
// that cannot be read is not worth refusing to start over, so it is reported as
// a warning and the plain name is used instead.
func deviceName(custom string, logger *slog.Logger) string {
	if custom != "" {
		return custom
	}

	host, err := hostname()
	if err != nil {
		logger.Warn("could not get host name, using the plain name instead", "err", err)
		return version.Name
	}
	if host = strings.TrimSpace(host); host == "" {
		logger.Warn("could not get host name, using the plain name instead", "err", "the host name is empty")
		return version.Name
	}
	return fmt.Sprintf("%s [%s]", version.Name, host)
}

// bannerInfo is everything the start-up banner reports about the configuration.
type bannerInfo struct {
	Name       string
	Root       string
	NoSSDP     bool
	Validation string
	Thumbnails string
	CacheDir   string
}

func printBanner(srv *upnp.Server, stats library.Stats, info bannerInfo, logger *slog.Logger) {
	base := srv.BaseURL()

	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n", version.Name, version.Version)
	fmt.Fprintf(&b, "  %s\n\n", strings.Repeat("\u2500", 46))
	fmt.Fprintf(&b, "  Media folder  %s\n", info.Root)
	fmt.Fprintf(&b, "  Device name   %s\n", info.Name)
	fmt.Fprintf(&b, "  Library       %d videos, %d folders, %d subtitles\n",
		stats.Videos, stats.Containers, stats.Subtitles)
	if stats.Rejected > 0 {
		fmt.Fprintf(&b, "                %d held back, see the web page\n", stats.Rejected)
	}
	fmt.Fprintf(&b, "  Address       %s\n", base)
	if info.NoSSDP {
		fmt.Fprintf(&b, "  Discovery     SSDP disabled\n")
	} else {
		fmt.Fprintf(&b, "  Discovery     SSDP multicast, port 1900\n")
	}
	fmt.Fprintf(&b, "  Validation    %s\n", info.Validation)
	fmt.Fprintf(&b, "  Thumbnails    %s\n", info.Thumbnails)
	if info.CacheDir != "" {
		fmt.Fprintf(&b, "  Cache         %s\n", info.CacheDir)
	}
	fmt.Fprintf(&b, "  Device UUID   %s\n\n", srv.UDN())

	if stats.Videos == 0 {
		fmt.Fprintf(&b, "  No videos found. Put .mp4 or .mkv files in this folder and press\n")
		fmt.Fprintf(&b, "  \"Rescan folder\" on the web page below.\n\n")
	}

	fmt.Fprintf(&b, "  On the TV      VLC \u2192 Local Network \u2192 %s\n", info.Name)
	fmt.Fprintf(&b, "  Web page       %s\n", base)
	fmt.Fprintf(&b, "  Stop           Ctrl+C\n\n")

	if _, err := os.Stdout.WriteString(b.String()); err != nil {
		logger.Debug("cannot write the banner", "err", err)
	}
}
