# devboxen-web (docs/web.md): one static binary, nothing else in the image. Built from the
# repository root: docker build -f web/Dockerfile .
FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/devboxen-web cmd/devboxen-web
RUN go vet ./cmd/devboxen-web && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /devboxen-web ./cmd/devboxen-web

FROM scratch
COPY --from=build /devboxen-web /devboxen-web
ENTRYPOINT ["/devboxen-web"]
