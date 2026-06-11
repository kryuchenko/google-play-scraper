package googleplayscraper

const BaseURL = "https://play.google.com"

// Sort options for reviews
type Sort int

const (
	SortHelpfulness Sort = 1
	SortNewest      Sort = 2
	SortRating      Sort = 3
)

// Collection types
type Collection string

const (
	CollectionTopFree  Collection = "TOP_FREE"
	CollectionTopPaid  Collection = "TOP_PAID"
	CollectionGrossing Collection = "GROSSING"
)

// clusterNames maps a Collection to the cluster identifier Google Play
// expects in the vyAe2 batchexecute payload.
var clusterNames = map[Collection]string{
	CollectionTopFree:  "topselling_free",
	CollectionTopPaid:  "topselling_paid",
	CollectionGrossing: "topgrossing",
}

// Category types
type Category string

const (
	// App categories
	CategoryApplication      Category = "APPLICATION"
	CategoryAndroidWear      Category = "ANDROID_WEAR"
	CategoryArtAndDesign     Category = "ART_AND_DESIGN"
	CategoryAutoAndVehicles  Category = "AUTO_AND_VEHICLES"
	CategoryBeauty           Category = "BEAUTY"
	CategoryBooksAndRef      Category = "BOOKS_AND_REFERENCE"
	CategoryBusiness         Category = "BUSINESS"
	CategoryComics           Category = "COMICS"
	CategoryCommunication    Category = "COMMUNICATION"
	CategoryDating           Category = "DATING"
	CategoryEducation        Category = "EDUCATION"
	CategoryEntertainment    Category = "ENTERTAINMENT"
	CategoryEvents           Category = "EVENTS"
	CategoryFamily           Category = "FAMILY"
	CategoryFinance          Category = "FINANCE"
	CategoryFoodAndDrink     Category = "FOOD_AND_DRINK"
	CategoryHealthAndFitness Category = "HEALTH_AND_FITNESS"
	CategoryHouseAndHome     Category = "HOUSE_AND_HOME"
	CategoryLibrariesAndDemo Category = "LIBRARIES_AND_DEMO"
	CategoryLifestyle        Category = "LIFESTYLE"
	CategoryMapsAndNav       Category = "MAPS_AND_NAVIGATION"
	CategoryMedical          Category = "MEDICAL"
	CategoryMusicAndAudio    Category = "MUSIC_AND_AUDIO"
	CategoryNewsAndMagazines Category = "NEWS_AND_MAGAZINES"
	CategoryParenting        Category = "PARENTING"
	CategoryPersonalization  Category = "PERSONALIZATION"
	CategoryPhotography      Category = "PHOTOGRAPHY"
	CategoryProductivity     Category = "PRODUCTIVITY"
	CategoryShopping         Category = "SHOPPING"
	CategorySocial           Category = "SOCIAL"
	CategorySports           Category = "SPORTS"
	CategoryTools            Category = "TOOLS"
	CategoryTravelAndLocal   Category = "TRAVEL_AND_LOCAL"
	CategoryVideoPlayers     Category = "VIDEO_PLAYERS"
	CategoryWatchFace        Category = "WATCH_FACE"
	CategoryWeather          Category = "WEATHER"

	// Game categories
	CategoryGame            Category = "GAME"
	CategoryGameAction      Category = "GAME_ACTION"
	CategoryGameAdventure   Category = "GAME_ADVENTURE"
	CategoryGameArcade      Category = "GAME_ARCADE"
	CategoryGameBoard       Category = "GAME_BOARD"
	CategoryGameCard        Category = "GAME_CARD"
	CategoryGameCasino      Category = "GAME_CASINO"
	CategoryGameCasual      Category = "GAME_CASUAL"
	CategoryGameEducational Category = "GAME_EDUCATIONAL"
	CategoryGameMusic       Category = "GAME_MUSIC"
	CategoryGamePuzzle      Category = "GAME_PUZZLE"
	CategoryGameRacing      Category = "GAME_RACING"
	CategoryGameRolePlaying Category = "GAME_ROLE_PLAYING"
	CategoryGameSimulation  Category = "GAME_SIMULATION"
	CategoryGameSports      Category = "GAME_SPORTS"
	CategoryGameStrategy    Category = "GAME_STRATEGY"
	CategoryGameTrivia      Category = "GAME_TRIVIA"
	CategoryGameWord        Category = "GAME_WORD"
)

// AllCountries is a snapshot of country codes where the Google Play Store is
// available, as ISO 3166-1 alpha-2 codes in lowercase — the same form the gl
// query parameter takes everywhere in this library.
//
// Google does not publish a machine-readable list of Play countries, so this is
// a hand-curated snapshot (taken 2026-06-11) drawn from the published Play
// "available countries" documentation and cross-referenced with ISO 3166-1.
// Coverage drifts as Google opens or closes markets; treat it as a sensible
// default sweep set, not an authoritative registry. A code being present here
// only means Play *generally* operates there — a given app may still be
// region-locked, which Availability reports per country.
//
// Markets without an official Play Store (e.g. cn, ir, kp, sy) are excluded, so
// a default AllCountries sweep never probes them. Pass them explicitly via
// AvailabilityOptions.Countries if you want to check a Play details page there
// anyway (the page still renders and usually reports NotInRegion).
var AllCountries = []string{
	"ae", "ag", "ai", "al", "am", "ao", "ar", "at", "au", "aw",
	"az", "ba", "bb", "bd", "be", "bf", "bg", "bh", "bj", "bm",
	"bn", "bo", "br", "bs", "bt", "bw", "by", "bz", "ca", "cd",
	"cg", "ch", "ci", "cl", "cm", "co", "cr", "cv", "cy", "cz",
	"de", "dk", "dm", "do", "dz", "ec", "ee", "eg", "es", "et",
	"fi", "fj", "fm", "fr", "ga", "gb", "gd", "ge", "gh", "gr",
	"gt", "gw", "gy", "hk", "hn", "hr", "ht", "hu", "id", "ie",
	"il", "in", "iq", "is", "it", "jm", "jo", "jp", "ke", "kg",
	"kh", "ki", "kn", "kr", "kw", "ky", "kz", "la", "lb", "lc",
	"li", "lk", "lr", "ls", "lt", "lu", "lv", "ly", "ma", "md",
	"me", "mg", "mk", "ml", "mm", "mn", "mo", "mt", "mu", "mv",
	"mw", "mx", "my", "mz", "na", "ne", "ng", "ni", "nl", "no",
	"np", "nz", "om", "pa", "pe", "pg", "ph", "pk", "pl", "pr",
	"pt", "pw", "py", "qa", "ro", "rs", "ru", "rw", "sa", "sb",
	"sc", "se", "sg", "si", "sk", "sl", "sn", "sr", "st", "sv",
	"sz", "tc", "td", "tg", "th", "tj", "tm", "tn", "to", "tr",
	"tt", "tw", "tz", "ua", "ug", "us", "uy", "uz", "vc", "ve",
	"vg", "vn", "vu", "ye", "za", "zm", "zw",
}

// AllCategories returns all known category IDs
var AllCategories = []Category{
	CategoryApplication,
	CategoryAndroidWear,
	CategoryArtAndDesign,
	CategoryAutoAndVehicles,
	CategoryBeauty,
	CategoryBooksAndRef,
	CategoryBusiness,
	CategoryComics,
	CategoryCommunication,
	CategoryDating,
	CategoryEducation,
	CategoryEntertainment,
	CategoryEvents,
	CategoryFamily,
	CategoryFinance,
	CategoryFoodAndDrink,
	CategoryHealthAndFitness,
	CategoryHouseAndHome,
	CategoryLibrariesAndDemo,
	CategoryLifestyle,
	CategoryMapsAndNav,
	CategoryMedical,
	CategoryMusicAndAudio,
	CategoryNewsAndMagazines,
	CategoryParenting,
	CategoryPersonalization,
	CategoryPhotography,
	CategoryProductivity,
	CategoryShopping,
	CategorySocial,
	CategorySports,
	CategoryTools,
	CategoryTravelAndLocal,
	CategoryVideoPlayers,
	CategoryWatchFace,
	CategoryWeather,
	CategoryGame,
	CategoryGameAction,
	CategoryGameAdventure,
	CategoryGameArcade,
	CategoryGameBoard,
	CategoryGameCard,
	CategoryGameCasino,
	CategoryGameCasual,
	CategoryGameEducational,
	CategoryGameMusic,
	CategoryGamePuzzle,
	CategoryGameRacing,
	CategoryGameRolePlaying,
	CategoryGameSimulation,
	CategoryGameSports,
	CategoryGameStrategy,
	CategoryGameTrivia,
	CategoryGameWord,
}
