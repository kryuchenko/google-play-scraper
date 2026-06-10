package googleplayscraper

import (
	"encoding/json"
	"regexp"
	"strings"
)

// scriptDataRegex matches the AF_initDataCallback script blocks embedded in
// Google Play HTML pages, capturing the ds:N key and its JSON data payload.
var scriptDataRegex = regexp.MustCompile(`AF_initDataCallback\(\{key:\s*'(ds:\d+)'.*?data:(.*?), sideChannel:`)

// parseDataBlocks extracts every AF_initDataCallback script block from a Google
// Play HTML page and returns its decoded JSON payload keyed by the ds:N
// identifier. Blocks whose data fails to unmarshal are silently skipped, mirroring
// the lenient parsing Google Play pages require.
func parseDataBlocks(body []byte) map[string]interface{} {
	dataBlocks := make(map[string]interface{})
	matches := scriptDataRegex.FindAllStringSubmatch(string(body), -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		key := match[1]
		dataStr := strings.TrimSpace(match[2])

		var data interface{}
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			continue
		}
		dataBlocks[key] = data
	}

	return dataBlocks
}
