# Upstream's Dockerfile, kept unchanged and still working.
#
# This fork exists because macOS cannot run Linux containers, so nothing here is
# used on a Mac. It is retained because the Linux side of this tree is untouched
# and still builds and runs exactly as upstream does; removing it would break
# that for no reason. Same goes for docker-compose.example.yml.
#
# Stage 1 (Build)
FROM golang:1.24.11-alpine AS builder

ARG VERSION
RUN apk add --update --no-cache git make mailcap
WORKDIR /app/
COPY go.mod go.sum /app/
RUN go mod download
COPY . /app/
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X github.com/pterodactyl/wings/system.Version=$VERSION" \
    -v \
    -trimpath \
    -o wings \
    wings.go
RUN echo "ID=\"distroless\"" > /etc/os-release

# Stage 2 (Final)
FROM gcr.io/distroless/static:latest
COPY --from=builder /etc/os-release /etc/os-release
COPY --from=builder /etc/mime.types /etc/mime.types

COPY --from=builder /app/wings /usr/bin/

ENTRYPOINT ["/usr/bin/wings"]
CMD ["--config", "/etc/pterodactyl/config.yml"]

EXPOSE 8080 2022
