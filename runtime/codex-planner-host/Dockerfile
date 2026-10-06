# Build only this pinned library host; the runtime contains no Codex CLI.
FROM rust:1.95.0-bookworm AS build
WORKDIR /host
ENV CARGO_BUILD_JOBS=1
COPY Cargo.toml Cargo.lock ./
COPY src ./src
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/host/target \
    cargo build --locked && strip target/debug/codex-planner-host && \
    cp target/debug/codex-planner-host /codex-planner-host

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 libgcc-s1 && \
    rm -rf /var/lib/apt/lists/* && mkdir /planner
COPY --from=build /codex-planner-host /usr/local/bin/codex-planner-host
WORKDIR /planner
ENTRYPOINT []
CMD ["/bin/sleep", "1200"]
