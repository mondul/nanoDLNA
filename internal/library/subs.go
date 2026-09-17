package library

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SubExts maps a lower-case subtitle extension to the MIME type used in the
// DLNA protocolInfo. Every value deliberately starts with "text/" because that
// is what VLC's UPnP browser looks for when it collects subtitle tracks:
//
//	if (strncmp(rez_type, "http-get:*:text/", 16) == 0)
//	    holder.addSlave(res_value, SLAVE_TYPE_SPU);
//
// (modules/services_discovery/upnp.cpp in both the 3.0.x and master branches.)
var SubExts = map[string]string{
	".srt":  "text/srt",
	".ass":  "text/x-ssa",
	".ssa":  "text/x-ssa",
	".vtt":  "text/vtt",
	".sub":  "text/plain", // MicroDVD; binary VobSub files are rejected below
	".smi":  "text/plain",
	".sami": "text/plain",
}

// subtitleDirNames are directory names whose contents are treated as a
// subtitle pool for videos in the parent directory rather than being exposed as
// browsable folders.
var subtitleDirNames = map[string]bool{
	"subs":        true,
	"sub":         true,
	"subtitles":   true,
	"subtitle":    true,
	"srt":         true,
	"subtitulos":  true,
	"sottotitoli": true,
	"untertitel":  true,
	"subtitres":   true,
	"legendas":    true,
}

func isSubtitleDirName(name string) bool {
	return subtitleDirNames[strings.ToLower(name)]
}

// subQualifiers are recognised sub-name tokens that are not language tags.
// The list is deliberately short: a token that is neither a language nor one of
// these makes the whole suffix unrecognised, which is what stops unrelated
// files from being attached to a video.
var subQualifiers = map[string]bool{
	"forced": true, "force": true, "forc": true,
	"sdh": true, "cc": true, "hearing": true, "hearingimpaired": true, "deaf": true,
	"full": true, "complete": true, "default": true, "def": true,
	"foreign": true, "signs": true, "songs": true, "commentary": true,
	"dub": true, "dubtitles": true, "normal": true, "norm": true,
}

// matchSubtitles finds subtitle files in pool that belong to the video at
// videoPath. Matching is deliberately conservative: a subtitle is only attached
// when its name is derived from the video's name by adding a recognised
// language tag or qualifier. That avoids pairing the wrong subtitle with a film
// just because the two files happen to share a folder.
func matchSubtitles(videoPath string, pool []string, pref []string) []Subtitle {
	fileName := filepath.Base(videoPath)
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)

	var out []Subtitle
	seen := map[string]bool{}

	for _, subPath := range pool {
		subExt := strings.ToLower(filepath.Ext(subPath))
		mime, ok := SubExts[subExt]
		if !ok {
			continue
		}
		subName := filepath.Base(subPath)
		stem := strings.TrimSuffix(subName, filepath.Ext(subName))

		var suffix string
		switch {
		case stem == base, stem == fileName:
			suffix = "" // Movie.srt or Movie.mkv.srt
		case strings.HasPrefix(stem, base+"."):
			suffix = stem[len(base)+1:] // Movie.en.srt
		case strings.HasPrefix(stem, fileName+"."):
			suffix = stem[len(fileName)+1:] // Movie.mkv.en.srt
		default:
			continue
		}

		lang, forced, sdh, recognised := parseSubSuffix(suffix)
		if !recognised {
			continue
		}
		if isBinarySubtitle(subPath) {
			continue
		}
		if seen[subPath] {
			continue
		}
		seen[subPath] = true
		out = append(out, Subtitle{
			Path:   subPath,
			Name:   subName,
			Lang:   lang,
			Mime:   mime,
			Forced: forced,
			SDH:    sdh,
		})
	}

	sortSubtitles(out, pref)
	return out
}

// parseSubSuffix interprets the part of a subtitle file name that follows the
// video's own name. It reports whether the suffix was recognised at all, which
// is what keeps unrelated files from being attached.
func parseSubSuffix(suffix string) (lang string, forced, sdh, recognised bool) {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return "", false, false, true
	}

	tokens := strings.FieldsFunc(suffix, func(r rune) bool {
		switch r {
		case '.', '_', ' ', '-':
			return true
		}
		return false
	})

	for _, tok := range tokens {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			continue
		}
		base := t
		if i := strings.IndexByte(t, '-'); i > 0 {
			base = t[:i]
		}
		if lang == "" {
			if code, ok := langAliases[base]; ok {
				lang = code
				recognised = true
				continue
			}
		}
		if subQualifiers[t] {
			recognised = true
			switch t {
			case "forced", "force", "forc":
				forced = true
			case "sdh", "cc", "hearing", "hearingimpaired", "deaf":
				sdh = true
			}
		}
	}
	return lang, forced, sdh, recognised
}

// sortSubtitles orders tracks so that the server's preferred languages come
// first, then untagged tracks, then everything else. Forced and SDH tracks sink
// below the full track for the same language.
func sortSubtitles(subs []Subtitle, pref []string) {
	sort.SliceStable(subs, func(i, j int) bool {
		a, b := subs[i], subs[j]
		if ra, rb := langRank(a.Lang, pref), langRank(b.Lang, pref); ra != rb {
			return ra < rb
		}
		if a.Forced != b.Forced {
			return !a.Forced
		}
		if a.SDH != b.SDH {
			return !a.SDH
		}
		if a.Lang != b.Lang {
			return a.Lang < b.Lang
		}
		return lessFold(a.Name, b.Name)
	})
}

func langRank(lang string, pref []string) int {
	if len(pref) == 0 {
		return 0
	}
	if lang == "" {
		return len(pref)
	}
	for i, p := range pref {
		if p == lang {
			return i
		}
	}
	return len(pref) + 1
}

// isBinarySubtitle reports whether a file looks like a binary subtitle format
// such as VobSub, which shares the .sub extension with MicroDVD text files and
// would confuse the player if advertised as text.
func isBinarySubtitle(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	if n <= 0 {
		return false
	}
	return bytes.IndexByte(buf[:n], 0) >= 0
}
