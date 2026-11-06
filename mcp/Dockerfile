# gpscrape-mcp in a container. Stdio by default, which is what MCP clients
# launch (`docker run -i --rm ghcr.io/kryuchenko/gpscrape-mcp`). For HTTP:
#   docker run --rm -p 8080:8080 -e GPSCRAPE_MCP_TOKEN=... \
#     ghcr.io/kryuchenko/gpscrape-mcp -http 0.0.0.0:8080
# Base images are pinned by digest (a tag can move; a digest cannot) and
# dependabot refreshes the pins.
# Build from this directory: docker build -t gpscrape-mcp mcp
# It resolves the root library from the module proxy (GOWORK=off), the way
# a `go install` user gets it, so a release must tag the root first.
# The build stage runs on the builder's own platform and cross-compiles, so
# a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY . .
ENV GOWORK=off CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/gpscrape-mcp ./cmd/gpscrape-mcp

# static: no shell, no package manager, but CA certificates and a nonroot user,
# which is all a client of play.google.com needs.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/gpscrape-mcp /gpscrape-mcp
LABEL io.modelcontextprotocol.server.name="io.github.kryuchenko/gpscrape"
LABEL org.opencontainers.image.source="https://github.com/kryuchenko/google-play-scraper"
LABEL org.opencontainers.image.description="MCP server for read-only Google Play Store data"
LABEL org.opencontainers.image.licenses="MIT"
ENTRYPOINT ["/gpscrape-mcp"]
