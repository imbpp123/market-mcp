FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build

WORKDIR /src
ENV GOTOOLCHAIN=local
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/market-mcp ./cmd/market-mcp

FROM scratch

COPY --from=build /out/market-mcp /market-mcp

USER 65532:65532
EXPOSE 8082
STOPSIGNAL SIGTERM
ENTRYPOINT ["/market-mcp"]
