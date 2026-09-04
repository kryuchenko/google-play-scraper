package googleplayscraper

import "slices"

// Coverage dictionaries
// ======================
//
// CategoryApps multiplies its reach by issuing many independent searches. The
// quality of that multiplier depends on the breadth of search terms: terms that
// map to distinct sub-genres and mechanics surface different slices of the
// catalogue with little overlap. The dictionaries below are hand-curated per
// category, richest for the GAME_* categories where sub-genre vocabulary is
// deep and well established.

// CoverageLocales is a ready-made set of country/language pairs chosen for high
// catalogue dispersion: large, distinct app markets whose top charts and search
// results overlap little. Callers can pass all of them, or a prefix slice, as
// CoverageOptions.Locales.
var CoverageLocales = []Locale{
	{Country: "us", Lang: "en"},
	{Country: "gb", Lang: "en"},
	{Country: "in", Lang: "en"},
	{Country: "br", Lang: "pt"},
	{Country: "ru", Lang: "ru"},
	{Country: "jp", Lang: "ja"},
	{Country: "de", Lang: "de"},
	{Country: "kr", Lang: "ko"},
	{Country: "id", Lang: "id"},
	{Country: "mx", Lang: "es"},
	{Country: "fr", Lang: "fr"},
	{Country: "tr", Lang: "tr"},
	{Country: "vn", Lang: "vi"},
	{Country: "th", Lang: "th"},
}

// defaultSearchTerms returns the curated coverage vocabulary for a category, or
// nil if none is defined. CategoryApps falls back to this when
// CoverageOptions.SearchTerms is unset.
func defaultSearchTerms(cat Category) []string {
	return categorySearchTerms[cat]
}

// categorySearchTerms maps a category to its coverage search vocabulary. GAME_*
// entries enumerate sub-genres and mechanics; the handful of non-game entries
// are starter sets, sufficient to demonstrate the multiplier without claiming
// exhaustiveness.
var categorySearchTerms = map[Category][]string{
	CategoryGameAction: {
		"shooter", "fps", "third person shooter", "battle royale", "zombie",
		"zombie survival", "ninja", "samurai", "fighting", "beat em up",
		"hack and slash", "run", "endless runner", "survival shooter",
		"war", "sniper", "gun", "shoot em up", "bullet hell", "platformer",
		"stealth", "action rpg", "robot", "mecha", "tank", "battle",
		"gladiator", "boxing", "martial arts", "kung fu", "assassin",
		"commando", "soldier", "battlefield", "apocalypse", "monster hunter",
		"superhero", "spaceship shooter", "side scroller", "metroidvania",
	},
	CategoryGameAdventure: {
		"adventure", "point and click", "escape room", "hidden object",
		"interactive story", "visual novel", "open world", "exploration",
		"survival", "story game", "mystery", "detective", "treasure hunt",
		"quest", "narrative", "choices", "rpg adventure", "dungeon",
		"pirate", "fantasy adventure", "horror adventure", "walking simulator",
		"text adventure", "sandbox", "crafting survival",
	},
	CategoryGameArcade: {
		"arcade", "endless runner", "jumper", "flappy", "tap", "reflex",
		"retro arcade", "pixel arcade", "classic arcade", "pinball",
		"brick breaker", "snake", "pac man style", "shoot em up arcade",
		"coin pusher", "claw machine", "bounce", "dodge", "stack",
		"merge arcade", "idle arcade", "hyper casual", "one button",
	},
	CategoryGameBoard: {
		"board game", "chess", "checkers", "ludo", "backgammon", "dominoes",
		"go", "reversi", "othello", "snakes and ladders", "monopoly style",
		"tic tac toe", "carrom", "mahjong", "shogi", "xiangqi",
		"tabletop", "dice board", "scrabble style", "battleship",
	},
	CategoryGameCard: {
		"card game", "solitaire", "poker", "blackjack", "rummy", "uno style",
		"spades", "hearts", "bridge", "gin rummy", "freecell", "klondike",
		"spider solitaire", "deck builder", "ccg", "tcg", "trading card",
		"baccarat", "war card", "crazy eights", "euchre", "pinochle",
	},
	CategoryGameCasino: {
		"casino", "slots", "slot machine", "roulette", "poker casino",
		"blackjack casino", "bingo", "baccarat", "keno", "scratch card",
		"video poker", "craps", "casino royale", "vegas slots",
		"fruit machine", "jackpot", "lottery",
	},
	CategoryGameCasual: {
		"casual", "match 3", "bubble shooter", "merge", "idle", "clicker",
		"coloring", "puzzle casual", "decorate", "design home", "dress up",
		"cooking", "farm", "garden", "pet", "tycoon casual", "sort",
		"jigsaw", "connect", "blast", "pop", "candy", "jewel", "gummy",
	},
	CategoryGameEducational: {
		"educational", "kids learning", "abc", "alphabet", "counting",
		"math kids", "spelling", "phonics", "preschool", "toddler",
		"learn colors", "learn shapes", "science kids", "geography kids",
		"coding for kids", "typing", "memory kids", "brain kids",
	},
	CategoryGameMusic: {
		"music game", "rhythm", "piano tiles", "guitar", "drums", "dance",
		"beat", "karaoke", "dj", "music maker", "tap rhythm", "edm",
		"singing", "band", "instrument", "music quiz",
	},
	CategoryGamePuzzle: {
		"puzzle", "jigsaw", "sudoku", "crossword", "word puzzle", "logic",
		"brain teaser", "match 3", "block puzzle", "sliding puzzle",
		"nonogram", "tangram", "maze", "escape puzzle", "physics puzzle",
		"riddle", "iq test", "hidden object puzzle", "connect dots",
		"pipe", "unblock", "tetris style", "2048", "merge puzzle",
	},
	CategoryGameRacing: {
		"racing", "car racing", "bike racing", "drift", "drag racing",
		"motorcycle", "kart", "formula", "off road", "rally", "truck racing",
		"street racing", "stunt", "traffic racer", "moto", "atv",
		"boat racing", "endless racing", "police chase", "tuning",
	},
	CategoryGameRolePlaying: {
		"rpg", "role playing", "mmorpg", "action rpg", "turn based rpg",
		"jrpg", "fantasy rpg", "dungeon crawler", "idle rpg", "anime rpg",
		"open world rpg", "tactical rpg", "roguelike", "gacha", "hero",
		"summoner", "kingdom", "medieval rpg", "pixel rpg", "card rpg",
		"survival rpg", "monster collector", "isekai",
	},
	CategoryGameSimulation: {
		"simulation", "tycoon", "city builder", "farm sim", "life sim",
		"business sim", "flight simulator", "driving simulator",
		"truck simulator", "construction", "idle tycoon", "sandbox sim",
		"restaurant", "hospital sim", "airport", "train sim", "factory",
		"god game", "pet sim", "vehicle sim", "job simulator",
	},
	CategoryGameSports: {
		"sports", "football", "soccer", "basketball", "baseball", "cricket",
		"tennis", "golf", "hockey", "volleyball", "bowling", "billiards",
		"pool", "darts", "rugby", "boxing sports", "mma", "skateboard",
		"archery", "fishing", "table tennis", "badminton", "wrestling",
	},
	CategoryGameStrategy: {
		"strategy", "tower defense", "rts", "real time strategy", "4x",
		"war strategy", "base building", "empire", "kingdom strategy",
		"turn based strategy", "moba", "auto chess", "civilization style",
		"tactics", "card strategy", "idle strategy", "city defense",
		"clan war", "conquest", "tower defence", "chess strategy",
	},
	CategoryGameTrivia: {
		"trivia", "quiz", "general knowledge", "guess", "word quiz",
		"picture quiz", "logo quiz", "music quiz", "movie quiz", "iq quiz",
		"brain quiz", "history quiz", "geography quiz", "puzzle quiz",
	},
	CategoryGameWord: {
		"word game", "crossword", "word search", "anagram", "scrabble style",
		"word connect", "word puzzle", "spelling", "vocabulary", "hangman",
		"word cookies", "letter", "wordle style", "boggle", "scramble",
	},

	// Non-game starter sets.
	CategoryApplication: {
		"app", "tool", "utility", "productivity", "editor", "manager",
		"tracker", "scanner", "converter", "calculator", "organizer",
	},
	CategoryTools: {
		"tool", "utility", "cleaner", "battery", "vpn", "file manager",
		"flashlight", "qr scanner", "wifi", "backup", "antivirus",
		"keyboard", "launcher", "screen recorder", "calculator",
	},
	CategoryFinance: {
		"finance", "banking", "wallet", "budget", "expense tracker",
		"investing", "crypto", "stocks", "loan", "tax", "accounting",
		"money manager", "payment", "trading",
	},
	CategoryPhotography: {
		"camera", "photo editor", "collage", "filter", "selfie", "beauty cam",
		"photo collage", "background remover", "retouch", "panorama",
		"gif maker", "photo frame",
	},
	CategorySocial: {
		"social", "chat", "messenger", "dating", "friends", "community",
		"video chat", "group chat", "social network", "live stream",
	},
}

// ReviewLanguages returns the set of hl values that return a distinct body of
// reviews.
//
// It returns a fresh copy on every call. The list is a fact about Google's
// endpoint rather than a setting, and an exported slice is a global variable
// any caller can reorder, truncate or empty for every other caller in the
// process -- a corruption that shows up much later, as a sweep that quietly
// skips a language.
//
// Reviews are the one place where the language parameter does not filter but
// *partitions*: hl selects which corpus is served, and the corpora do not
// overlap. Measured on com.spotify.music -- the first fifty codes returned
// 1,991 reviews of which 1,991 were distinct, with no id appearing under two
// codes. So the union over this list is how to read all of an app's reviews,
// and reading one language reads one slice of them.
//
// The country parameter does nothing here at all. Checked on com.spotify.music
// and, because it is the case that matters, on kz.kaspi.mobile, a bank used
// almost entirely from Kazakhstan: ru/kz, ru/ru and ru/us return the same
// reviews id for id, and so do kk/kz and kk/us. Nor is there a country
// anywhere in the response -- a review carries seventeen fields and none of
// them is geographic. Reviews cannot be selected by market, and the closest
// available proxy is language, which is not the same thing: ru covers Russia,
// Belarus, Ukraine and Kazakhstan together and cannot be split.
//
// Every code below was exercised. Some codes are aliases and are deliberately
// absent: tg and tk are served the Russian corpus verbatim (30 of 30
// identical), ga and cy the English one. A caller passing its own list should
// expect the occasional alias and deduplicate.
//
// The list is not a claim to be exhaustive. It is the set that was checked,
// which is a different and more useful thing than a list copied from a locale
// table and never exercised.
func ReviewLanguages() []string { return slices.Clone(reviewLanguages) }

var reviewLanguages = []string{
	// Large markets first, so a caller taking a prefix slice gets the reach.
	"en", "es", "pt", "ru", "de", "fr", "it", "ja", "ko", "zh",
	"tr", "id", "vi", "th", "hi", "ar", "pl", "nl", "sv", "da",
	"fi", "no", "cs", "el", "he", "hu", "ro", "uk", "bg", "hr",
	"sr", "sk", "sl", "lt", "lv", "et", "fa", "bn", "ta", "te",
	"ml", "mr", "gu", "kn", "pa", "ur", "ms", "tl", "sw", "af",
	// Central Asia, the Caucasus and the Balkans. These were missing from the
	// first pass, which meant "all languages" skipped Kazakh entirely -- on an
	// app whose users are mostly Kazakhstani that is not a rounding error.
	"kk", "az", "uz", "ky", "ka", "hy", "be", "mk", "sq", "bs",
	// Everything else checked and found distinct.
	"is", "mn", "ne", "si", "km", "lo", "my", "am", "eu", "gl", "ca",
}
