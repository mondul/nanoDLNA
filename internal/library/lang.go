package library

// langGroups maps a canonical ISO 639-1 language tag to every token that may
// stand for it in a subtitle file name: the ISO 639-1 code, the ISO 639-2/B and
// /T codes, and the common English language name.
//
// Keeping the table in this grouped form makes a token mapping to two different
// languages impossible to express by accident.
var langGroups = map[string][]string{
	"af":  {"af", "afr", "afrikaans"},
	"am":  {"am", "amh", "amharic"},
	"ar":  {"ar", "ara", "arabic"},
	"az":  {"az", "aze", "azerbaijani"},
	"be":  {"be", "bel", "belarusian"},
	"bg":  {"bg", "bul", "bulgarian"},
	"bn":  {"bn", "ben", "bengali"},
	"bo":  {"bo", "bod", "tib", "tibetan"},
	"bs":  {"bs", "bos", "bosnian"},
	"ca":  {"ca", "cat", "catalan"},
	"cs":  {"cs", "cze", "ces", "czech"},
	"cy":  {"cy", "wel", "cym", "welsh"},
	"da":  {"da", "dan", "danish"},
	"de":  {"de", "ger", "deu", "german", "deutsch"},
	"el":  {"el", "gre", "ell", "greek"},
	"en":  {"en", "eng", "english"},
	"eo":  {"eo", "epo", "esperanto"},
	"es":  {"es", "spa", "spanish", "castellano", "espagnol"},
	"et":  {"et", "est", "estonian"},
	"eu":  {"eu", "baq", "eus", "basque"},
	"fa":  {"fa", "per", "fas", "farsi", "persian"},
	"fi":  {"fi", "fin", "finnish"},
	"fil": {"fil", "tgl", "tl", "tagalog", "filipino"},
	"fo":  {"fo", "fao", "faroese"},
	"fr":  {"fr", "fre", "fra", "french", "francais", "français"},
	"ga":  {"ga", "gle", "irish"},
	"gl":  {"gl", "glg", "galician"},
	"gu":  {"gu", "guj", "gujarati"},
	"he":  {"he", "heb", "iw", "hebrew"},
	"hi":  {"hi", "hin", "hindi"},
	"hr":  {"hr", "hrv", "croatian"},
	"hu":  {"hu", "hun", "hungarian"},
	"hy":  {"hy", "arm", "hye", "armenian"},
	"id":  {"id", "ind", "in", "indonesian"},
	"is":  {"is", "ice", "isl", "icelandic"},
	"it":  {"it", "ita", "italian", "italiano"},
	"ja":  {"ja", "jpn", "japanese"},
	"ka":  {"ka", "geo", "kat", "georgian"},
	"kk":  {"kk", "kaz", "kazakh"},
	"km":  {"km", "khm", "khmer"},
	"kn":  {"kn", "kan", "kannada"},
	"ko":  {"ko", "kor", "korean"},
	"ku":  {"ku", "kur", "kurdish"},
	"ky":  {"ky", "kir", "kirghiz"},
	"la":  {"la", "lat", "latin"},
	"lo":  {"lo", "lao"},
	"lt":  {"lt", "lit", "lithuanian"},
	"lv":  {"lv", "lav", "latvian"},
	"mk":  {"mk", "mac", "mkd", "macedonian"},
	"ml":  {"ml", "mal", "malayalam"},
	"mn":  {"mn", "mon", "mongolian"},
	"mr":  {"mr", "mar", "marathi"},
	"ms":  {"ms", "may", "msa", "malay"},
	"mt":  {"mt", "mlt", "maltese"},
	"my":  {"my", "bur", "mya", "burmese"},
	"nb":  {"nb", "nob", "norwegianbokmal"},
	"ne":  {"ne", "nep", "nepali"},
	"nl":  {"nl", "dut", "nld", "dutch"},
	"nn":  {"nn", "nno", "nynorsk"},
	"no":  {"no", "nor", "norwegian"},
	"pa":  {"pa", "pan", "punjabi"},
	"pl":  {"pl", "pol", "polish"},
	"ps":  {"ps", "pus", "pashto"},
	"pt":  {"pt", "por", "portuguese", "brasileiro", "brazilian"},
	"ro":  {"ro", "rum", "ron", "romanian"},
	"ru":  {"ru", "rus", "russian"},
	"si":  {"si", "sin", "sinhala"},
	"sk":  {"sk", "slo", "slk", "slovak"},
	"sl":  {"sl", "slv", "slovenian"},
	"sq":  {"sq", "alb", "sqi", "albanian"},
	"sr":  {"sr", "srp", "serbian"},
	"sv":  {"sv", "swe", "swedish"},
	"sw":  {"sw", "swa", "swahili"},
	"ta":  {"ta", "tam", "tamil"},
	"te":  {"te", "tel", "telugu"},
	"tg":  {"tg", "tgk", "tajik"},
	"th":  {"th", "tha", "thai"},
	"tr":  {"tr", "tur", "turkish"},
	"tt":  {"tt", "tat", "tatar"},
	"uk":  {"uk", "ukr", "ukrainian"},
	"ur":  {"ur", "urd", "urdu"},
	"uz":  {"uz", "uzb", "uzbek"},
	"vi":  {"vi", "vie", "vietnamese"},
	"yi":  {"yi", "yid", "yiddish"},
	"zh":  {"zh", "chi", "zho", "chinese", "mandarin", "cantonese", "zhcn", "zhtw"},
	"zu":  {"zu", "zul", "zulu"},
}

// langAliases is the flattened lookup used while matching subtitle file names.
var langAliases = buildLangAliases()

func buildLangAliases() map[string]string {
	total := 0
	for _, tokens := range langGroups {
		total += len(tokens)
	}
	m := make(map[string]string, total)
	for code, tokens := range langGroups {
		for _, t := range tokens {
			if prev, dup := m[t]; dup && prev != code {
				panic("library: language token " + t + " maps to both " + prev + " and " + code)
			}
			m[t] = code
		}
	}
	return m
}
