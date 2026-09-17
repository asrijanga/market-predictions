# The DuckDB driver needs cgo, so this is a two-stage build with a toolchain
# in the builder and a glibc base at runtime rather than a scratch image.
FROM golang:1.24 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/mktpredict ./cmd/mktpredict

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*

# The analysis database and the HTTP response cache live here. Mount a
# volume on it to keep them across deploys.
RUN useradd --create-home --uid 10001 mkt \
 && mkdir -p /data && chown mkt:mkt /data
USER mkt
VOLUME /data
ENV XDG_CACHE_HOME=/data

COPY --from=build /out/mktpredict /usr/local/bin/mktpredict

EXPOSE 8080
# 0.0.0.0 rather than localhost, so the container is reachable.
CMD ["mktpredict", "serve", "-addr", "0.0.0.0:8080"]
