# nanoDLNA

A tiny DLNA/UPnP media server in a single Go binary. Run it inside a folder of
films and your television can browse and play them — **including their external
`.srt` subtitles**.

No configuration, no database, no dependencies. It reads the folder you start it
in, works out the metadata from the container headers, matches subtitle files to
videos, and announces itself on the local network.

```
nanoDLNA 1.0.0
──────────────────────────────────────────────

  Media folder  /Users/you/Movies
  Device name   nanoDLNA
  Library       42 videos, 7 folders, 38 subtitles
  Address       http://192.168.1.20:8200
  Discovery     SSDP multicast, port 1900
  Device UUID   uuid:0bf6bd04-d693-53c9-8ac1-f6a1b87e284c

  On the TV      VLC → Local Network → nanoDLNA
  Web page       http://192.168.1.20:8200
  Stop           Ctrl+C
```

## Quick start

```sh
cd /path/to/your/movies
nanoDLNA
```

Or point it at a folder, which makes it possible to **drag a folder onto the
executable** and have nanoDLNA serve it:

```sh
nanoDLNA /path/to/your/movies
```

With no argument the folder you are standing in is served. Either way the
address to open on the television is printed at start-up.

Then on the television open **VLC → Local Network** (sometimes called *Browse* →
*Local Network* → *Universal Plug'n'Play*) and pick **nanoDLNA**. Open a film and
the subtitle menu will already list the `.srt` files sitting next to it.

To build it:

```sh
make build
```

Use `make build` rather than a bare `go build`. It stamps the version derived
from the git tag into the binary with `-ldflags`, so `nanoDLNA -version` matches
the release. A plain `go build` produces a binary reporting the development
default from `internal/version/version.go`, which is not the tag, and that is
easy to mistake for a stale build.

## How the subtitles work

This is the part that most simple DLNA servers get wrong, so it is worth
explaining what nanoDLNA does and why.

VLC's UPnP browser reads subtitle tracks out of the DIDL-Lite metadata the server
returns when browsing, and it looks in **three** different places
(`modules/services_discovery/upnp.cpp`, identical in the 3.0.x and master
branches):

| Mechanism | What VLC looks for |
| --- | --- |
| Samsung extensions | a `<sec:CaptionInfo>` element, else `<sec:CaptionInfoEx>` |
| pv namespace | a `pv:subtitleFileUri` attribute on the video `<res>` |
| Generic | any `<res>` whose `protocolInfo` begins with `http-get:*:text/` |

nanoDLNA emits **all three**, pointing at the same URL, for every video that has
subtitles. That is deliberate: VLC collects the results into a `std::set` keyed
by URL, so the same track referenced three times still produces exactly one entry
in its subtitle menu. Clients that only understand one of the conventions still
find the subtitle, and VLC does not show duplicates.

For a film with three subtitle files you get three tracks, on every client.

### Which subtitle becomes the default

`sec:CaptionInfo` (the track VLC preselects), `pv:subtitleFileUri` and the first
`<res>` all point at the highest-ranked subtitle. Ranking is:

1. languages listed in `-sub-lang`, most preferred first;
2. when no preference is given, untagged files such as `Movie.srt` come first;
3. complete tracks before `forced` before `SDH`.

Subtitle files are streamed as UTF-8 whatever their original encoding, so no
mojibake on the television.

## Subtitle matching

A subtitle is attached to a video when its name begins with the video's name and
what follows is a recognised language tag or qualifier. Everything below matches
`Movie.mkv`:

```
Movie.srt                    Movie.en.srt          Movie.eng.srt
Movie.English.srt            Movie.en-US.srt       Movie.pt-BR.srt
Movie.en.forced.srt          Movie.en.sdh.srt      Movie.mkv.srt
Subs/Movie.de.srt            Subtitles/Movie.fr.srt
```

Supported extensions: `.srt`, `.ass`, `.ssa`, `.vtt`, `.sub` (MicroDVD), `.smi`.
Binary VobSub `.sub` files are detected and skipped rather than advertised as
text.

Matching is intentionally conservative. `Movie.2020.srt` belongs to
`Movie.2020.mkv` and is **not** also attached to `Movie.mkv`, and a file named
`Something Else.srt` is never guessed onto a video just because it shares a
folder. Subtitle files are not shown as browsable folders on the TV; a `Subs` or
`Subtitles` folder is folded into the matching pool for its parent directory.

Filenames are also matched with `_` separators, and language detection knows
ISO 639-1 codes (`it`), ISO 639-2/B and /T codes (`ita`, `fre`, `fra`) and
English language names (`italian`).

## What it serves

| Format | Extensions |
| --- | --- |
| Video | `mp4`, `m4v`, `mkv`, `webm`, `avi`, `divx`, `mov`, `mpg`, `mpeg`, `vob`, `ts`, `m2ts`, `mts`, `wmv`, `asf`, `flv`, `ogv`, `3gp` |
| Subtitles | `srt`, `ass`, `ssa`, `vtt`, `sub`, `smi` |

Duration and resolution are read straight from the MP4/MOV box tree and the
Matroska EBML headers (plus AVI), in pure Go — no `ffmpeg` needed. Files whose
metadata cannot be determined are still served; the television simply does not
get a duration up front.

Durations, resolutions and file sizes are shown as DLNA `res` attributes, byte
ranges are supported so seeking works, and empty folders are pruned so the TV
does not show directories with nothing to play.

## Options

```
nanoDLNA [options] [folder]

  -name string       name shown on the television (default "nanoDLNA [host name]")
  -port int          HTTP port; 0 lets the system choose (default 8200)
  -iface string      network interface name or local IP to advertise
  -sub-lang string   preferred subtitle languages, e.g. "it,en"
  -charset string    subtitle encoding: auto, utf-8, cp1252, latin1
  -log string        log level: debug, info, warn, error (default "info")
  -no-ssdp           do not advertise with SSDP discovery
  -version           print the version and exit
```

Useful variations:

```sh
nanoDLNA ~/Films                       # serve another folder
nanoDLNA -name "Living Room" ~/Films   # options come before the folder
nanoDLNA -log debug                    # see every browse request and play
nanoDLNA -port 0                       # let the OS pick the HTTP port
nanoDLNA -iface en0                    # pin announcements to one interface
```

Without `-name` the server is listed as `nanoDLNA [host name]`, so that two
machines running it on the same network can be told apart in the player's list.
If the host name cannot be read, nanoDLNA says so and falls back to plain
`nanoDLNA`.

There is also a small web page on the printed address, which lists every video
with its subtitles, links directly to the streams, and has a **Rescan folder**
button for when you add films while the server is running.

## Troubleshooting

**The television does not list the server.**
Allow incoming connections for `nanoDLNA` in the firewall — macOS asks the first
time you run it, and if you declined, re-enable it under *System Settings →
Network → Firewall → Options*. Make sure the TV is on the same subnet, and that
the router is not blocking multicast or client isolation. Open the printed web
address on another device to confirm the server is reachable.

**The server is listed but a film will not play.**
`-log debug` prints every request the television makes. If nothing arrives when
you press play, the TV is refusing the file rather than failing to fetch it; the
usual cause is a codec the player cannot decode.

**No subtitles appear in the menu.**
Check the web page: if a video lists no subtitles there, the file name did not
match — see the rules above. If the subtitles are listed but VLC does not offer
them, use *Open Network Stream* on the TV with the media URL shown on the web
page, which bypasses UPnP browsing entirely. Some television firmwares also
disable external subtitle loading for network streams in their settings.

**Discovery stops working after a while.**
Discovery announcements are repeated every ten minutes and on every rescan. If a
television caches aggressively, restart nanoDLNA and it will re-announce
immediately.

## How it works

- **SSDP** — binds UDP 1900, answers `M-SEARCH` for `ssdp:all`,
  `upnp:rootdevice`, the MediaServer device type and both service types, with a
  random delay inside the `MX` window as the specification requires. Announces
  `ssdp:alive` on start and periodically, and `ssdp:byebye` on Ctrl+C. Searches
  for a newer version of a type it implements (`MediaServer:2`) are answered too.
- **HTTP** — device and service descriptions, the two SOAP control endpoints,
  GENA event subscription endpoints, byte-range video streaming, and subtitle
  delivery. Video is served with `transferMode.dlna.org: Streaming` and
  `DLNA.ORG_OP=01` so the player knows it can seek.
- **ContentDirectory** — `Browse` (both `BrowseMetadata` and
  `BrowseDirectChildren`, with paging), `GetSearchCapabilities`,
  `GetSortCapabilities` and `GetSystemUpdateID`. Every action declared in the
  service description is implemented, so clients never hit an unexpected fault.
- **Objects** are identified by small integers, which keeps DIDL and URLs simple
  and widely compatible. The device UUID is derived from the media folder path,
  so the television keeps recognising the same server across restarts and
  reboots.

## Development

```sh
make build       # build ./nanoDLNA with the version stamped in
make test        # go test ./...
make race        # go test -race ./...
make check       # gofmt, go vet, tests, and the stale-binary check
make stale       # only the stale-binary check
make lint        # check that the commits follow Conventional Commits
make hooks       # install the commit-msg hook in this clone
make cross       # build the five release targets into dist/
```

The test suite includes a faithful reimplementation of VLC's subtitle-collection
logic, driven through real SOAP `Browse` responses, so the DIDL layout cannot
regress without a test failing.

```
internal/avmeta     MP4/MOV, Matroska/WebM and AVI header parsing
internal/library    filesystem scan, tree building, subtitle matching
internal/didl       DIDL-Lite generation
internal/upnp       SSDP, HTTP, SOAP, GENA, media and subtitle serving
internal/version    program identity
scripts/            commit linting and release tooling
```

Requires Go 1.22 or newer. The only imports are the standard library.

## Releases

Pushing to `main` runs `.github/workflows/release.yml`, which builds and
publishes a release when the push contains something worth releasing.

**The version number is derived from the commit messages**, so every commit must
follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>[optional scope][!]: <description>

feat(player): add gapless playback
fix: stop the scan from following symlinks
feat(api)!: drop the v1 endpoints
```

The types are `feat`, `fix`, `build`, `chore`, `ci`, `docs`, `style`,
`refactor`, `perf`, `test` and `revert`. A breaking change is marked with `!`
before the colon or a `BREAKING CHANGE:` footer.

| Commits since the last tag | Next version |
| --- | --- |
| any breaking change | major, `1.4.2` → `2.0.0` |
| any `feat` | minor, `1.4.2` → `1.5.0` |
| any `fix`, `perf` or `revert` | patch, `1.4.2` → `1.4.3` |
| only `docs`, `chore`, `test`, `ci`, `build`, `refactor` | no release |

The first release is `1.0.0`, because there is no baseline to bump from. The
number is injected at link time rather than committed back, so the source
default of `1.0.0` is only a development placeholder and no bot commits appear
in the history. `make print-version` shows what a local build will report.

Each release carries a binary for **Linux x86-64 and ARM64, macOS ARM64, and
Windows x86-64 and ARM64** as `.tar.gz` or `.zip`, with `README.md`, `LICENSE`
and a `SHA256SUMS` file. The notes list every commit grouped by type, with the
full commit message, so a release says what actually changed rather than only
naming the commits.

To skip the workflow for a push, put `[skip ci]` in the commit message. GitHub
honours that itself, and the workflow checks for it too.

### Keeping the messages honest

Three things enforce the format, at different moments:

- **`make hooks`** installs a `commit-msg` hook that refuses a commit outright.
  This is the only place a message can be rejected *before* it exists, and it is
  per clone: run it once after cloning, or accept that a bad message is only
  caught afterwards.
- **The Commit messages workflow** checks every push and pull request, so a
  message that slipped past the hook shows up red.
- **A ruleset** is what makes GitHub refuse the push itself. A workflow cannot:
  by the time it runs, the push has already been accepted. To get a hard
  rejection, protect `main` and require the `Conventional Commits` check, which
  also means changes arrive through pull requests instead of direct pushes.

The release workflow and the hook run the same `scripts/commit-lint.sh`, so they
can never disagree about what a valid message is.

## License

MIT — see [LICENSE](LICENSE).
