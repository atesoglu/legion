# syntax=docker/dockerfile:1

# One Dockerfile for every Rust service (ADR-016 section 3), parameterised and
# built from the repository root.
#
# CRATE is the binary crate name, which ADR-016 section 1 fixes as
# legion-<identifier>; the identifier itself is only needed for the image
# label, so this file takes the crate and derives nothing.
#
#   docker build -f deploy/docker/rust.Dockerfile \
#       --build-arg CRATE=legion-sentinel -t legion/sentinel:dev .

ARG RUST_VERSION=1.88

FROM rust:${RUST_VERSION}-slim-bookworm AS chef
WORKDIR /src

# See the note in go.Dockerfile: a network that terminates TLS presents its own
# certificate for crates.io, which the image does not trust. Site-specific, so
# it is mounted as a build secret and never reaches the final image.
RUN --mount=type=secret,id=ca_bundle \
    if [ -s /run/secrets/ca_bundle ]; then \
        cp /run/secrets/ca_bundle /usr/local/share/ca-certificates/corporate.crt && \
        update-ca-certificates; \
    fi && \
    cargo install cargo-chef --locked

FROM chef AS planner
COPY . .
RUN cargo chef prepare --recipe-path recipe.json

FROM chef AS build
COPY --from=planner /src/recipe.json recipe.json

# Dependencies compile in their own layer, keyed on the recipe rather than on
# the sources. Eight crates share one lockfile, so without this every source
# change rebuilds every dependency -- and slow image builds are skipped image
# builds (ADR-016 section 3).
RUN --mount=type=secret,id=ca_bundle \
    if [ -s /run/secrets/ca_bundle ]; then \
        cp /run/secrets/ca_bundle /usr/local/share/ca-certificates/corporate.crt && \
        update-ca-certificates; \
    fi && \
    cargo chef cook --release --recipe-path recipe.json

COPY . .

ARG CRATE
RUN test -n "${CRATE}" || (echo "CRATE build argument is required" >&2; exit 1)
RUN cargo build --release --bin "${CRATE}" && cp "target/release/${CRATE}" /out-service

# cc rather than static: the release profile links against glibc. No shell and
# no package manager either way (security-boundaries.md section 4).
FROM gcr.io/distroless/cc-debian12:nonroot

ARG CRATE
LABEL org.opencontainers.image.title="legion/${CRATE}"
LABEL org.opencontainers.image.source="https://github.com/atesoglu/legion"

COPY --from=build /out-service /legion

USER nonroot:nonroot
ENTRYPOINT ["/legion"]
