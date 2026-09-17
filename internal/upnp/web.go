package upnp

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"nanodlna/internal/library"
	"nanodlna/internal/version"
)

// indexTemplate is a small status page served at "/". It exists so that the
// user can confirm the server is running and check the exact URL to type into
// a player when discovery is blocked by the network.
var indexTemplate = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<style>
:root { color-scheme: dark; }
body { margin: 0; padding: 2rem 1.25rem 4rem; background: #121822; color: #e6edf3;
       font: 15px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 60rem; margin: 0 auto; }
h1 { font-size: 1.5rem; margin: 0 0 .25rem; display: flex; align-items: center; gap: .6rem; }
h1 img { width: 32px; height: 32px; border-radius: .35rem; }
h2 { font-size: 1.05rem; margin: 2rem 0 .75rem; color: #9fb0c3; text-transform: uppercase;
     letter-spacing: .06em; font-weight: 600; }
a { color: #35d0c0; }
code { background: #1b2431; padding: .15rem .4rem; border-radius: .3rem; font-size: .9em; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: .5rem .6rem; border-bottom: 1px solid #232d3d; vertical-align: top; }
th { color: #9fb0c3; font-weight: 600; font-size: .82rem; text-transform: uppercase; letter-spacing: .05em; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: .35rem 1.25rem; margin: 0; }
dt { color: #9fb0c3; }
dd { margin: 0; }
.sub { color: #8fa3b8; font-size: .9em; }
.empty { padding: 1.25rem; border: 1px dashed #33415a; border-radius: .6rem; color: #9fb0c3; }
form { margin-top: 1.5rem; }
button { background: #35d0c0; color: #07211f; border: 0; border-radius: .4rem;
         padding: .55rem 1.1rem; font: inherit; font-weight: 600; cursor: pointer; }
</style>
</head>
<body>
<main>
  <h1>{{if .IconURL}}<img src="{{.IconURL}}" alt="">{{end}}{{.Name}}</h1>
  <p class="sub">nanoDLNA {{.Version}} &middot; up {{.Uptime}}</p>

  <h2>Server</h2>
  <dl>
    <dt>Address</dt><dd><a href="{{.BaseURL}}">{{.BaseURL}}</a></dd>
    <dt>Media folder</dt><dd><code>{{.RootPath}}</code></dd>
    <dt>Device UUID</dt><dd><code>{{.UDN}}</code></dd>
    <dt>Library</dt><dd>{{.Stats.Videos}} videos in {{.Stats.Containers}} folders, {{.Stats.Subtitles}} subtitles{{if .Stats.Rejected}}, {{.Stats.Rejected}} held back</dd>{{end}}
    <dt>Discovery</dt><dd>{{if .SSDPOn}}SSDP advertising on {{.IfaceName}}{{else}}disabled{{end}}</dd>
    <dt>Scanned in</dt><dd>{{.ScanTime}}</dd>
  </dl>

  <form method="post" action="/rescan"><button type="submit">Rescan folder</button></form>

  <h2>Videos</h2>
  {{if .Videos}}
  <table>
    <thead><tr><th>Title</th><th>Details</th><th>Subtitles</th><th></th></tr></thead>
    <tbody>
    {{range .Videos}}
      <tr>
        <td>{{.Title}}<div class="sub">{{.Rel}}</div></td>
        <td class="sub">{{.Details}}</td>
        <td class="sub">{{range .Subs}}<div>{{.}}</div>{{else}}&mdash;{{end}}</td>
        <td><a href="{{.MediaURL}}">play</a></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <p class="empty">No videos found in this folder yet. Drop some .mp4 or .mkv files next to
  nanoDLNA and press &ldquo;Rescan folder&rdquo;.</p>
  {{end}}

  {{if .Rejected}}
  <h2>Not ready</h2>
  <p class="sub">These files were found but could not be read, so they are not offered to
  the television. A file that is still downloading appears here until it is complete; press
  &ldquo;Rescan folder&rdquo; once it has finished.</p>
  <table>
    <thead><tr><th>Title</th><th>Details</th><th>Reason</th></tr></thead>
    <tbody>
    {{range .Rejected}}
      <tr>
        <td>{{.Title}}<div class="sub">{{.Rel}}</div></td>
        <td class="sub">{{.Details}}</td>
        <td class="sub">{{.Reason}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{end}}
</main>
</body>
</html>
`))

type indexVideo struct {
	Title    string
	Rel      string
	Details  string
	Subs     []string
	MediaURL string
}

type indexRejected struct {
	Title   string
	Rel     string
	Details string
	Reason  string
}

type indexView struct {
	Name      string
	Version   string
	BaseURL   string
	RootPath  string
	UDN       string
	Uptime    string
	ScanTime  string
	Stats     library.Stats
	SSDPOn    bool
	IfaceName string
	IconURL   string
	Videos    []indexVideo
	Rejected  []indexRejected
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !allowGetHead(w, r) {
		return
	}

	base := s.BaseURL()
	root := s.lib.Root()
	rootPath := s.cfg.RootPath
	if root != nil {
		rootPath = root.Path
	}

	view := indexView{
		Name:      s.cfg.Name,
		Version:   version.Version,
		BaseURL:   base,
		RootPath:  rootPath,
		UDN:       s.udn,
		Uptime:    time.Since(s.started).Round(time.Second).String(),
		Stats:     s.lib.Stats(),
		SSDPOn:    s.ssdp != nil,
		IfaceName: interfaceName(s.ssdpInterface()),
		IconURL:   iconPath,
	}
	view.ScanTime = view.Stats.Elapsed.Round(time.Millisecond).String()

	for _, v := range s.lib.Videos() {
		row := indexVideo{
			Title:    v.Title,
			Rel:      library.DisplayPath(rootPath, v.Path),
			Details:  describeVideo(v),
			MediaURL: s.mediaURL(base, v),
		}
		for _, sub := range v.Subs {
			row.Subs = append(row.Subs, sub.Label())
		}
		view.Videos = append(view.Videos, row)
	}

	for _, r := range s.lib.Rejected() {
		view.Rejected = append(view.Rejected, indexRejected{
			Title:   r.Title,
			Rel:     library.DisplayPath(rootPath, r.Path),
			Details: describeRejected(r),
			Reason:  r.Reason,
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	if err := indexTemplate.Execute(w, view); err != nil {
		s.log.Warn("rendering the status page failed", "err", err)
	}
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := s.Rescan()
	if err != nil {
		s.log.Error("rescan failed", "err", err)
		http.Error(w, "rescan failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("rescan complete",
		"videos", stats.Videos,
		"folders", stats.Containers,
		"subtitles", stats.Subtitles,
		"took", stats.Elapsed.Round(time.Millisecond))

	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) ssdpInterface() *net.Interface {
	if s.ssdp == nil {
		return nil
	}
	return s.ssdp.iface
}

// describeRejected renders the size and age of a file that is being held back,
// which is what tells a person whether a download is still moving.
func describeRejected(r library.Rejected) string {
	parts := make([]string, 0, 2)
	if r.Size > 0 {
		parts = append(parts, humanSize(r.Size))
	}
	if !r.ModTime.IsZero() {
		parts = append(parts, "changed "+formatAge(time.Since(r.ModTime)))
	}
	if len(parts) == 0 {
		return "\u2014"
	}
	return strings.Join(parts, " \u00b7 ")
}

// formatAge renders how long ago something happened, coarsely.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// describeVideo renders the size, duration and resolution of a video.
func describeVideo(v *library.Node) string {
	parts := make([]string, 0, 3)
	if v.Size > 0 {
		parts = append(parts, humanSize(v.Size))
	}
	if d := v.Info.Duration; d > 0 {
		parts = append(parts, formatClock(d))
	}
	if v.Info.Width > 0 && v.Info.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", v.Info.Width, v.Info.Height))
	}
	if len(parts) == 0 {
		return "\u2014"
	}
	return strings.Join(parts, " \u00b7 ")
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

func formatClock(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
