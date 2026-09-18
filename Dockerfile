# The DuckDB driver needs cgo, so this is a two-stage build with a toolchain
# in the builder and a glibc base at runtime rather than a scratch image.
#
# Both stages name their Debian release, and they must match. A cgo binary
# links against the builder's glibc and will not start on an older one, and
# the unqualified golang tag follows Debian's newest release -- so leaving
# it implicit means an upstream base bump silently produces an image that
# builds, pushes, and then dies on boot with a loader error.
FROM golang:1.24-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/mktpredict ./cmd/mktpredict

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata util-linux \
 && rm -rf /var/lib/apt/lists/*

# The analysis database and the HTTP response cache live here. Mount a
# volume on it to keep them across deploys.
RUN useradd --create-home --uid 10001 mkt \
 && mkdir -p /data && chown mkt:mkt /data
VOLUME /data
ENV XDG_CACHE_HOME=/data

COPY --from=build /out/mktpredict /usr/local/bin/mktpredict
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Prove the binary loads in this image. A missing symbol version is
# invisible until the process starts, which on a deploy means a machine
# that boots, exits 1, and fails its health checks with nothing in the
# deploy output to say why. Running it once here makes that a build error
# instead, minutes earlier and with the reason on screen.
RUN mktpredict > /dev/null

# The container starts as root only long enough to hand the volume to mkt;
# the entrypoint drops to that user before running anything.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

EXPOSE 8080
# 0.0.0.0 rather than localhost, so the container is reachable.
CMD ["mktpredict", "serve", "-addr", "0.0.0.0:8080"]
