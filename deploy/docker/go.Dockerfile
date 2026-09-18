# syntax=docker/dockerfile:1

# One Dockerfile for every Go service (ADR-016 section 3), parameterised and
# built from the repository root so that the context is obvious rather than
# surprising.
#
# SERVICE names the image; SOURCE locates the main package. Both are needed
# because neither derives from the other in either direction --
# investigation-controller is built from investigation/controller -- and both
# are read from deploy/services.yaml, so no mapping is encoded here.
#
#   docker build -f deploy/docker/go.Dockerfile \
#       --build-arg SERVICE=gateway --build-arg SOURCE=gateway \
#       -t legion/gateway:dev .

ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Dependencies resolve in their own layer: a source change must not re-download
# the module cache.
COPY go.mod go.sum ./

# A corporate network that terminates TLS presents its own certificate for
# proxy.golang.org, which the image does not trust, and the module download
# fails with "certificate signed by unknown authority". The CA is mounted as a
# build secret rather than baked in: it is site-specific, it does not belong in
# the repository, and it must not reach the final image.
#
#   docker build --secret id=ca_bundle,src=/path/to/corporate-ca.crt ...
#
# Builds on a network that does not intercept TLS pass no secret and skip this.
RUN --mount=type=secret,id=ca_bundle \
    if [ -s /run/secrets/ca_bundle ]; then \
        cp /run/secrets/ca_bundle /usr/local/share/ca-certificates/corporate.crt && \
        update-ca-certificates; \
    fi && \
    go mod download

COPY . .

ARG SOURCE
# A missing SOURCE would otherwise build the repository root and fail somewhere
# less obvious.
RUN test -n "${SOURCE}" || (echo "SOURCE build argument is required" >&2; exit 1)

# CGO off so the result is static and can run on a distroless/static base with
# no libc at all. -trimpath keeps build paths out of the binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./${SOURCE}

# No shell, no package manager, non-root by default: security-boundaries.md
# section 4's image requirements, which apply to every zone rather than only
# the agent sandbox.
FROM gcr.io/distroless/static-debian12:nonroot

ARG SERVICE
LABEL org.opencontainers.image.title="legion/${SERVICE}"
LABEL org.opencontainers.image.source="https://github.com/atesoglu/legion"

COPY --from=build /out/service /legion

USER nonroot:nonroot
ENTRYPOINT ["/legion"]
