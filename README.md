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

Then on the television open **VLC → Local Network** (sometimes called *Browse* →
*Local Network* → *Universal Plug'n'Play*) and pick **nanoDLNA**. Open a film and
the subtitle menu will already list the `.srt` files sitting next to it.

To build it:

```sh
go build -o nanoDLNA .          # or: make
```

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

  -dir string        folder to serve (the current folder by default)
  -name string       name shown on the television (default "nanoDLNA")
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
nanoDLNA -name "Living Room" -sub-lang it,en
nanoDLNA -log debug                    # see every browse request and play
nanoDLNA -port 0                       # let the OS pick the HTTP port
nanoDLNA -iface en0                    # pin announcements to one interface
```

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
make test        # go test ./...
make race        # go test -race ./...
make check       # gofmt, go vet, tests
make cross       # build for macOS, Linux and Windows
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
```

Requires Go 1.22 or newer. The only imports are the standard library.

## License

MIT — see [LICENSE](LICENSE).
