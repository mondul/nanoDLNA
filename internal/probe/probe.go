// Package probe decides whether a media file can be served, using ffprobe when
// it is installed.
//
// The point is the answer the player would reach anyway: a file whose container
// cannot be read is one the television cannot play, so it is better hidden than
// offered and then failed. Torrent clients make this worth doing, because they
// reserve the full space for a file long before it holds anything.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"nanodlna/internal/tools"
)

// Level is how much work goes into deciding.
type Level int

const (
	// Off serves every file, which is what happens with ffprobe missing.
	Off Level = iota
	// Container reads the file's headers. This catches a file that has been
	// reserved but not written, and one whose index lives at the end and has
	// not arrived.
	//
	// It does not catch a web-optimised MP4 that is half downloaded, because
	// its index sits at the front and is readable from the first moment.
	Container
	// Content also decodes a frame near the start and near the end, which does
	// catch that case. It costs two more ffmpeg runs per file, once each, since
	// the answers are cached.
	Content
)

func (l Level) String() string {
	switch l {
	case Off:
		return "off"
	case Container:
		return "container"
	case Content:
		return "content"
	default:
		return "unknown"
	}
}

// ParseLevel reads a level name from the command line.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "none":
		return Off, nil
	case "container", "probe":
		return Container, nil
	case "content", "full":
		return Content, nil
	default:
		return Off, fmt.Errorf("unknown validation level %q (use off, container or content)", s)
	}
}

// Verdict is what was decided about one file.
type Verdict struct {
	// Serve is false when the file should not be offered to a player.
	Serve bool
	// Reason explains a refusal, in ffprobe's own words where possible.
	Reason string
	// Duration, Width and Height are filled in when the tool reported them, and
	// are otherwise zero, which means "unknown" rather than "none".
	Duration time.Duration
	Width    int
	Height   int
	// Cached reports that this answer came from a previous run.
	Cached bool
}

// Config configures a Prober.
type Config struct {
	// Level is how much checking to do. Off makes Check a no-op.
	Level Level
	// Probe is the ffprobe that was found, or nil.
	Probe *tools.Tool
	// Decode is the ffmpeg that was found, or nil. Content needs it.
	Decode *tools.Tool
	// Store is where verdicts are remembered between runs.
	Store *Store
	// Timeout bounds a single invocation.
	Timeout time.Duration
	// Revalidate ignores what the store already knows.
	Revalidate bool
	// Logger receives diagnostics. Nil means slog.Default().
	Logger *slog.Logger
}

// Prober checks files and remembers what it learned. It is safe for concurrent
// use, which matters because a scan probes files in parallel.
type Prober struct {
	level      Level
	probe      *tools.Tool
	decode     *tools.Tool
	store      *Store
	timeout    time.Duration
	revalidate bool
	log        *slog.Logger
}

// New prepares a Prober, lowering the level when the tools it needs are absent
// rather than failing.
func New(cfg Config) *Prober {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}

	level := cfg.Level
	switch {
	case level == Off:
	case cfg.Probe == nil:
		cfg.Logger.Warn("ffprobe was not found, so files will be served without being checked")
		level = Off
	case level == Content && cfg.Decode == nil:
		cfg.Logger.Warn("content validation needs ffmpeg, which was not found; falling back to checking containers only")
		level = Container
	}

	return &Prober{
		level:      level,
		probe:      cfg.Probe,
		decode:     cfg.Decode,
		store:      cfg.Store,
		timeout:    cfg.Timeout,
		revalidate: cfg.Revalidate,
		log:        cfg.Logger,
	}
}

// Level reports the level actually in force, which may be lower than the one
// that was asked for.
func (p *Prober) Level() Level { return p.level }

// Check decides whether a file may be served.
func (p *Prober) Check(path string, size int64, mod time.Time) Verdict {
	if p.level == Off {
		return Verdict{Serve: true}
	}

	// A stored yes is reusable while the file has not changed. A stored no is
	// not: a refusal is exactly the state a download changes, and a stalled
	// download can leave the size and time alone while still being worth
	// another look.
	if !p.revalidate && p.store != nil {
		if rec, ok := p.store.Get(path, size, mod); ok && rec.Valid {
			return rec.verdict(true)
		}
	}

	verdict := p.inspect(path)
	if p.store != nil {
		p.store.Put(path, size, mod, verdict)
	}
	return verdict
}

func (p *Prober) inspect(path string) Verdict {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	verdict, err := p.runProbe(ctx, path)
	if err != nil {
		return verdict
	}

	if p.level == Content {
		if reason := p.checkPayload(ctx, path, verdict.Duration); reason != "" {
			return Verdict{Serve: false, Reason: reason}
		}
	}
	return verdict
}

// runProbe asks ffprobe to read the container, returning a refusal with
// ffprobe's own explanation when it cannot.
func (p *Prober) runProbe(ctx context.Context, path string) (Verdict, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.probe.Path,
		"-v", "error",
		"-show_entries", "format=duration",
		"-show_entries", "stream=codec_type,width,height",
		"-of", "json",
		path,
	)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		reason := firstUsefulLine(stderr.String(), path)
		if reason == "" {
			reason = err.Error()
		}
		return Verdict{Serve: false, Reason: reason}, fmt.Errorf("ffprobe rejected the file: %s", reason)
	}

	verdict := Verdict{Serve: true}
	var parsed struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		// ffprobe accepted the file but said something unparseable, which is a
		// reason to serve it without metadata rather than to hide it.
		p.log.Debug("cannot read ffprobe output", "path", path, "err", err)
		return verdict, nil
	}

	if seconds, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && seconds > 0 {
		verdict.Duration = time.Duration(seconds * float64(time.Second))
	}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && stream.Width > 0 && stream.Height > 0 {
			verdict.Width, verdict.Height = stream.Width, stream.Height
			break
		}
	}
	return verdict, nil
}

// contentPositions are where the payload is sampled. The end matters more than
// the start: a partial download almost always plays its opening seconds.
var contentPositions = []float64{0.25, 0.95}

// checkPayload decodes a frame at each sample position, returning a reason when
// one cannot be read.
func (p *Prober) checkPayload(ctx context.Context, path string, duration time.Duration) string {
	if duration <= 0 {
		// There is nowhere to seek to, so the container check has to stand.
		return ""
	}
	for _, fraction := range contentPositions {
		at := time.Duration(float64(duration) * fraction)
		if reason := p.decodeFrame(ctx, path, at); reason != "" {
			return fmt.Sprintf("no readable frame at %s (%s)", at.Round(time.Second), reason)
		}
	}
	return ""
}

func (p *Prober) decodeFrame(ctx context.Context, path string, at time.Duration) string {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.decode.Path,
		"-v", "error",
		"-ss", strconv.FormatFloat(at.Seconds(), 'f', 3, 64),
		"-i", path,
		"-frames:v", "1",
		// image2, and not one of the cheaper sinks, because they are no good
		// here: a null, rawvideo, framecrc or md5 sink accepts zero frames and
		// exits successfully, so a truncated file passes the check and the
		// whole thing silently does nothing. image2 is the only sink tried that
		// fails when the frame it was promised is not there.
		"-f", "image2", "pipe:",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if reason := firstUsefulLine(stderr.String(), path); reason != "" {
			return reason
		}
		return err.Error()
	}
	return ""
}

// firstUsefulLine picks ffprobe or ffmpeg's own explanation out of its output.
//
// The tools print a diagnostic line and then a summary prefixed with the file
// name; the last non-empty line is the summary, and the file name is stripped
// from it so the message reads as a sentence.
func firstUsefulLine(output, path string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, path+": "); ok {
			return rest
		}
		// ffprobe sometimes prints the name with the other separator.
		if rest, ok := strings.CutPrefix(line, path); ok {
			return strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		}
		return line
	}
	return ""
}

// Save persists what has been learned. It is a no-op without a store.
func (p *Prober) Save() error {
	if p.store == nil {
		return nil
	}
	return p.store.Save()
}
