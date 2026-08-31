# Provide one reproducible tool image for source-mounted benchmark and assertion runs.
# jq and wget support E2E assertions and readiness checks, not the benchmark runtime itself.
FROM docker.io/library/golang:1.26.6-bookworm

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    GOCACHE=/var/cache/go-build \
    GOMODCACHE=/var/cache/go-mod

RUN apt-get update \
    && apt-get install --yes --no-install-recommends jq wget \
    && rm -rf -- /var/lib/apt/lists/* \
    && mkdir --parents /var/cache/go-build /var/cache/go-mod

WORKDIR /workspace

CMD ["go", "version"]
