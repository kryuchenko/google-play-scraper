package googleplayscraper

import "time"

// Review represents a single user review.
type Review struct {
	// ID is the opaque review identifier (a UUID-like string).
	ID string `json:"id" example:"8f3c1d2e-1a2b-4c5d-9e0f-112233445566"`
	// UserName is the review author's display name.
	UserName string `json:"userName" example:"Jane Doe"`
	// UserImage is the URL of the author's avatar.
	UserImage string `json:"userImage" format:"uri" example:"https://play-lh.googleusercontent.com/a/AGNmyx...=s64"`
	// Date is when the review was posted, as a timezone-aware timestamp
	// (serialized RFC 3339).
	Date time.Time `json:"date" format:"date-time" example:"2026-04-21T23:00:45.167+02:00"`
	// Score is the star rating, an integer 1..5.
	Score int `json:"score" minimum:"1" maximum:"5" example:"5"`
	// Text is the review body.
	Text string `json:"text" example:"Works great, exactly what I needed."`
	// ReplyDate is when the developer replied; zero/absent when there is no
	// reply (serialized RFC 3339).
	ReplyDate time.Time `json:"replyDate" format:"date-time"`
	// ReplyText is the developer's reply body; absent when there is no reply.
	ReplyText string `json:"replyText,omitempty"`
	// Version is the app version the review was left on; may be absent.
	Version string `json:"version,omitempty" example:"26.15.01.894202351"`
	// ThumbsUp is the count of users who found the review helpful.
	ThumbsUp int `json:"thumbsUp" minimum:"0" example:"16"`
	// URL is the canonical link to the review on the Play Store.
	URL string `json:"url" format:"uri" example:"https://play.google.com/store/apps/details?id=com.example&reviewId=8f3c1d2e"`
	// Criterias holds per-aspect sub-ratings (e.g. gameplay, graphics); present
	// mainly on games and absent on most apps.
	Criterias []Criteria `json:"criterias,omitempty"`
}

// Criteria represents a per-aspect review sub-rating (e.g. gameplay, graphics).
type Criteria struct {
	// Name is the aspect being rated, e.g. "Gameplay" or "Graphics".
	Name string `json:"name" example:"Gameplay"`
	// Rating is the aspect's star rating, an integer 1..5.
	Rating int `json:"rating" minimum:"1" maximum:"5" example:"4"`
}

// ReviewsResult contains one page of reviews plus the pagination token.
type ReviewsResult struct {
	// Reviews is the page of parsed reviews.
	Reviews []Review `json:"reviews"`
	// NextToken is the continuation token for the next batchexecute page; empty
	// when there are no more reviews.
	NextToken string `json:"nextToken,omitempty" example:"CroBIrEBAU60USc0jm2Ps4haLPoSm2pV"`
}

// ReviewOptions configures the reviews request
type ReviewOptions struct {
	Lang        string
	Country     string
	Sort        Sort
	Count       int
	NextToken   string
	FilterScore int // Filter by score: 1, 2, 3, 4, or 5 (0 = all)
}

// DefaultReviewOptions returns sensible defaults
func DefaultReviewOptions() ReviewOptions {
	return ReviewOptions{
		Lang:    "en",
		Country: "us",
		Sort:    SortNewest,
		Count:   150,
	}
}

// App represents the full app-detail model parsed from the GET
// /store/apps/details listing (the ds:5 AF_initDataCallback block).
type App struct {
	// AppID is the app package name (the listing's id query parameter).
	AppID string `json:"appId" example:"com.google.android.apps.maps"`
	// Title is the localized app name.
	Title string `json:"title" example:"Google Maps"`
	// Summary is the short tagline shown under the title. It may contain HTML
	// entities exactly as Google serves them (e.g. "&amp;").
	Summary string `json:"summary" example:"Real-time GPS navigation &amp; local suggestions"`
	// Description is the long description with HTML stripped to plain text;
	// newlines are preserved.
	Description string `json:"description"`
	// DescriptionHTML is the long description with Google's original inline
	// markup preserved (mostly <br> tags and HTML entities).
	DescriptionHTML string `json:"descriptionHTML"`
	// Developer is the developer's display name.
	Developer string `json:"developer" example:"Google LLC"`
	// DeveloperID is the developer listing id used by GET /store/apps/developer
	// (a numeric id or a quoted name).
	DeveloperID string `json:"developerId" example:"Google LLC"`
	// DeveloperEmail is the developer's contact email; may be empty.
	DeveloperEmail string `json:"developerEmail" format:"email" example:"apps-help@google.com"`
	// DeveloperWebsite is the developer's website URL; may be empty.
	DeveloperWebsite string `json:"developerWebsite" format:"uri" example:"http://maps.google.com/about/"`
	// DeveloperAddress is the developer's postal address; may be empty.
	DeveloperAddress string `json:"developerAddress"`
	// Icon is the app icon URL.
	Icon string `json:"icon" format:"uri" example:"https://play-lh.googleusercontent.com/B8pdO_2K5nBsF0g1h6dKwV"`
	// Score is the average star rating, a float in [0,5] at full precision
	// (ScoreText holds the rounded one-decimal label Google displays).
	Score float64 `json:"score" minimum:"0" maximum:"5" example:"4.626984"`
	// ScoreText is the rounded, one-decimal rating label as displayed, e.g.
	// "4.6"; empty when the app has no rating.
	ScoreText string `json:"scoreText" example:"4.6"`
	// Ratings is the total number of star ratings.
	Ratings int `json:"ratings" minimum:"0" example:"19445897"`
	// Reviews is the number of written text reviews (a subset of Ratings).
	Reviews int `json:"reviews" minimum:"0" example:"677911"`
	// Histogram is the rating-count breakdown by star level, indexed
	// [0]=1-star .. [4]=5-star. Values are counts, not percentages, and need not
	// sum exactly to Ratings.
	Histogram [5]int `json:"histogram" example:"5452870,1679546,461292,1082704,9500000"`
	// Price is the purchase price in major currency units (already divided down
	// from Google's micros). 0 for free apps; see Currency for the unit.
	Price float64 `json:"price" minimum:"0" example:"0"`
	// PriceText is the formatted price label (e.g. "$4.99"); empty for free apps.
	PriceText string `json:"priceText" example:""`
	// Currency is the ISO 4217 code for Price/OriginalPrice, e.g. "USD", "EUR".
	Currency string `json:"currency" example:"USD"`
	// Free is true when Price == 0.
	Free bool `json:"free" example:"true"`
	// Installs is the human-formatted install count with grouping separators and
	// a trailing "+", exactly as displayed.
	Installs string `json:"installs" example:"1,000,000,000+"`
	// MinInstalls is the lower bound of the install bucket as an integer.
	MinInstalls int64 `json:"minInstalls" format:"int64" minimum:"0" example:"1000000000"`
	// MaxInstalls is Google's estimated actual install count (an upper bound,
	// strictly >= MinInstalls).
	MaxInstalls int64 `json:"maxInstalls" format:"int64" minimum:"0" example:"2263522953"`
	// Genre is the localized primary category name.
	Genre string `json:"genre" example:"Travel & Local"`
	// GenreID is the stable, uppercased category id (a Play category enum value).
	// See the categories list for the full set; "GAME_*" prefixes a game genre.
	GenreID string `json:"genreId" example:"TRAVEL_AND_LOCAL"`
	// Categories is the list of category names the app is filed under.
	Categories []string `json:"categories"`
	// Version is the current version string; may be empty when Google reports
	// "Varies with device".
	Version string `json:"version" example:"1.329.0.1"`
	// AndroidVersion is the minimum supported Android version; may be empty when
	// Google reports "Varies with device".
	AndroidVersion string `json:"androidVersion" example:"6.0"`
	// ContentRating is the age/content rating label, e.g. "Everyone" or
	// "USK: Ages 18+".
	ContentRating string `json:"contentRating" example:"Everyone"`
	// Released is the original release date as a Unix epoch in SECONDS, e.g.
	// 1352989068 (= 2012-11-15). 0 when absent. Symmetric with Updated.
	Released int64 `json:"released" format:"int64" minimum:"0" example:"1352989068"`
	// Updated is the last-updated time as a Unix epoch in SECONDS. 0 when absent.
	Updated int64 `json:"updated" format:"int64" minimum:"0" example:"1779963225"`
	// URL is the canonical Play Store listing URL.
	URL string `json:"url" format:"uri" example:"https://play.google.com/store/apps/details?id=com.king.candycrushsaga"`
	// Screenshots is the list of screenshot image URLs.
	Screenshots []string `json:"screenshots"`
	// Video is the YouTube trailer player URL; absent on most non-game listings.
	Video string `json:"video,omitempty" format:"uri"`
	// VideoImage is the trailer poster image URL; absent when there is no trailer.
	VideoImage string `json:"videoImage,omitempty" format:"uri"`
	// HeaderImage is the feature-graphic banner URL; absent on some listings.
	HeaderImage string `json:"headerImage,omitempty" format:"uri" example:"https://play-lh.googleusercontent.com/UVo1hFs93u3MCQMQo6_KoKrX"`
	// PreviewVideo is the autoplay mp4 preview clip URL; absent when there is none.
	PreviewVideo string `json:"previewVideo,omitempty" format:"uri"`
	// PrivacyPolicy is the developer's privacy-policy URL; may be absent.
	PrivacyPolicy string `json:"privacyPolicy,omitempty" format:"uri" example:"http://www.google.com/policies/privacy"`
	// Available is true when the listing was reachable and parsed (a 404/region
	// block yields false).
	Available bool `json:"available" example:"true"`

	// Monetization

	// AdSupported is true when the listing declares "Contains ads".
	AdSupported bool `json:"adSupported" example:"true"`
	// OffersIAP is true when the listing offers in-app purchases (derived from
	// the presence of IAPRange).
	OffersIAP bool `json:"offersIAP" example:"true"`
	// IAPRange is the human-readable in-app-purchase price range; absent when the
	// app has no IAP.
	IAPRange string `json:"IAPRange,omitempty" example:"$0.99 - $149.99 per item"`
	// OriginalPrice is the pre-discount price in major currency units (micros
	// divided down); present only while a promotional discount is active.
	OriginalPrice float64 `json:"originalPrice,omitempty" minimum:"0" example:"9.99"`
	// DiscountEndDate is the discount expiry as a Unix epoch in SECONDS; present
	// only while a discount is active, otherwise 0/absent.
	DiscountEndDate int64 `json:"discountEndDate,omitempty" format:"int64" minimum:"0" example:"1735689600"`

	// Distribution / availability

	// IsAvailableInPlayPass is true when the app is included in Google Play Pass.
	IsAvailableInPlayPass bool `json:"isAvailableInPlayPass" example:"false"`
	// Preregister is true for an unreleased app open for pre-registration.
	Preregister bool `json:"preregister" example:"false"`
	// EarlyAccessEnabled is true when the listing is in early-access state.
	EarlyAccessEnabled bool `json:"earlyAccessEnabled" example:"false"`

	// Content & changelog

	// RecentChanges is the latest "What's new" changelog text; may be absent.
	RecentChanges string `json:"recentChanges,omitempty"`
	// ContentRatingDescription is the qualifier shown next to ContentRating
	// (e.g. "Gambling with Cash Payouts"); may be absent.
	ContentRatingDescription string `json:"contentRatingDescription,omitempty" example:"Gambling with Cash Payouts"`

	// Developer (EU DSA trader info; absent for non-EU traders)

	// DeveloperInternalID is Google's internal developer id; present only for EU
	// DSA trader listings.
	DeveloperInternalID string `json:"developerInternalID,omitempty"`
	// DeveloperLegalName is the trader's registered legal name (EU DSA only).
	DeveloperLegalName string `json:"developerLegalName,omitempty"`
	// DeveloperLegalEmail is the trader's legal contact email (EU DSA only).
	DeveloperLegalEmail string `json:"developerLegalEmail,omitempty" format:"email"`
	// DeveloperLegalAddress is the trader's registered address (EU DSA only).
	DeveloperLegalAddress string `json:"developerLegalAddress,omitempty"`
	// DeveloperLegalPhoneNumber is the trader's contact phone number (EU DSA only).
	DeveloperLegalPhoneNumber string `json:"developerLegalPhoneNumber,omitempty"`
}
