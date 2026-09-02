// Not a real module. Its only job is to keep 14.8MB of recorded pages out of
// the zip every consumer of this library downloads: the module zip format
// omits any directory that contains a go.mod, which is the same rule that
// already excludes apidoc/ and lightfeed/.
//
// The go tool ignores testdata/ when matching packages, so this file is
// invisible to build, vet and test; the fixtures stay on disk and the suite
// reads them exactly as before.
module github.com/kryuchenko/google-play-scraper/testdata

go 1.25
