package googleplayscraper_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// ExampleStatusError shows how to distinguish HTTP status failures (e.g. a
// missing app vs. rate limiting) by unwrapping the error with errors.As.
func ExampleStatusError() {
	client := googleplayscraper.NewClient()

	_, err := client.App(context.Background(), "com.does.not.exist", googleplayscraper.AppOptions{})

	var statusErr *googleplayscraper.StatusError
	switch {
	case errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound:
		fmt.Println("app not found")
	case errors.As(err, &statusErr) && statusErr.Code == http.StatusTooManyRequests:
		fmt.Println("rate limited; back off and retry")
	case err != nil:
		fmt.Println("other error:", err)
	default:
		fmt.Println("ok")
	}
}
