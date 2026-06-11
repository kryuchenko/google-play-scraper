package googleplayscraper

import "time"

// Review represents a single user review
type Review struct {
	ID        string     `json:"id"`
	UserName  string     `json:"userName"`
	UserImage string     `json:"userImage"`
	Date      time.Time  `json:"date"`
	Score     int        `json:"score"`
	Text      string     `json:"text"`
	ReplyDate time.Time  `json:"replyDate,omitempty"`
	ReplyText string     `json:"replyText,omitempty"`
	Version   string     `json:"version,omitempty"`
	ThumbsUp  int        `json:"thumbsUp"`
	URL       string     `json:"url"`
	Criterias []Criteria `json:"criterias,omitempty"`
}

// Criteria represents review criteria (e.g., gameplay, graphics)
type Criteria struct {
	Name   string `json:"name"`
	Rating int    `json:"rating"`
}

// ReviewsResult contains reviews and pagination token
type ReviewsResult struct {
	Reviews   []Review `json:"reviews"`
	NextToken string   `json:"nextToken,omitempty"`
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

// App represents application details
type App struct {
	AppID            string   `json:"appId"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	Description      string   `json:"description"`
	DescriptionHTML  string   `json:"descriptionHTML"`
	Developer        string   `json:"developer"`
	DeveloperID      string   `json:"developerId"`
	DeveloperEmail   string   `json:"developerEmail"`
	DeveloperWebsite string   `json:"developerWebsite"`
	DeveloperAddress string   `json:"developerAddress"`
	Icon             string   `json:"icon"`
	Score            float64  `json:"score"`
	ScoreText        string   `json:"scoreText"`
	Ratings          int      `json:"ratings"`
	Reviews          int      `json:"reviews"`
	Histogram        [5]int   `json:"histogram"`
	Price            float64  `json:"price"`
	PriceText        string   `json:"priceText"`
	Currency         string   `json:"currency"`
	Free             bool     `json:"free"`
	Installs         string   `json:"installs"`
	MinInstalls      int64    `json:"minInstalls"`
	MaxInstalls      int64    `json:"maxInstalls"`
	Genre            string   `json:"genre"`
	GenreID          string   `json:"genreId"`
	Categories       []string `json:"categories"`
	Version          string   `json:"version"`
	AndroidVersion   string   `json:"androidVersion"`
	ContentRating    string   `json:"contentRating"`
	Released         string   `json:"released"`
	Updated          int64    `json:"updated"`
	URL              string   `json:"url"`
	Screenshots      []string `json:"screenshots"`
	Video            string   `json:"video,omitempty"`
	VideoImage       string   `json:"videoImage,omitempty"`
	HeaderImage      string   `json:"headerImage,omitempty"`
	PreviewVideo     string   `json:"previewVideo,omitempty"`
	PrivacyPolicy    string   `json:"privacyPolicy,omitempty"`
	Available        bool     `json:"available"`

	// Monetization
	AdSupported     bool    `json:"adSupported"`
	OffersIAP       bool    `json:"offersIAP"`
	IAPRange        string  `json:"IAPRange,omitempty"`
	OriginalPrice   float64 `json:"originalPrice,omitempty"`
	DiscountEndDate int64   `json:"discountEndDate,omitempty"`

	// Distribution / availability
	IsAvailableInPlayPass bool `json:"isAvailableInPlayPass"`
	Preregister           bool `json:"preregister"`
	EarlyAccessEnabled    bool `json:"earlyAccessEnabled"`

	// Content & changelog
	RecentChanges            string `json:"recentChanges,omitempty"`
	ContentRatingDescription string `json:"contentRatingDescription,omitempty"`

	// Developer (EU DSA trader info; absent for non-EU traders)
	DeveloperInternalID       string `json:"developerInternalID,omitempty"`
	DeveloperLegalName        string `json:"developerLegalName,omitempty"`
	DeveloperLegalEmail       string `json:"developerLegalEmail,omitempty"`
	DeveloperLegalAddress     string `json:"developerLegalAddress,omitempty"`
	DeveloperLegalPhoneNumber string `json:"developerLegalPhoneNumber,omitempty"`
}
