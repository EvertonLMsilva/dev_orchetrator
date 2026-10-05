# P6.2 controlled egress

Tinyproxy 1.11.3, official release/tag:
https://github.com/tinyproxy/tinyproxy/releases/tag/1.11.3

Artifact: https://github.com/tinyproxy/tinyproxy/releases/download/1.11.3/tinyproxy-1.11.3.tar.gz

SHA256 (GitHub release asset digest, verified during image build):
`9bcf46db1a2375ff3e3d27a41982f1efec4706cce8899ff9f33323a8218f7592`

Upstream license: GPL-2.0-or-later. The image includes COPYING and upstream
documentation. Both build and runtime bases are digest-pinned; no package manager
or floating Tinyproxy dependency is used. The runtime does not download images
or dependencies.

Explicit provisioning from repository root:

```sh
docker build -t dev-orchestrator-egress-proxy:1.11.3 internal/infrastructure/testdata/egress-proxy
```

The infrastructure constructor `NewAuthenticatedDockerCodexExecutorRuntime`
requires a Docker driver, an explicitly authorized `AuthorizedCodexHome` and
exact lowercase hostnames. No default allowlist or wildcard is supplied. It
copies trusted configuration; application/task/model fields cannot choose it.

Each session owns two generated networks: one internal network for Codex and
Tinyproxy, and one external network for Tinyproxy only. Codex retains its sole
authorized workspace bind. Tinyproxy has no host bind or published port; it
runs as UID/GID 65532 with a read-only root filesystem, dropped capabilities
and private tmpfs configuration. Cleanup removes the workload, proxy and both
owned networks, including partial provisioning failures/cancellation.

`FilterURLs On`, anchored `^hostname:443$` rules, `FilterDefaultDeny Yes`, and
`ConnectPort 443` permit only exact CONNECT authorities. Plain HTTP URLs are
denied even for allowed hostnames. HTTPS/WSS tunnel bytes retain end-to-end
TLS; redirect hosts require a new allowed CONNECT. No TLS interception occurs.
Workload external DNS upstream is restricted to its own loopback; Docker's
embedded DNS resolves the private proxy alias. The proxy resolves destinations.
Raw proxy diagnostics are piped to a bounded filter that publishes only bare
DNS CONNECT hostnames/ports and denied hostnames on CONNECT 443. URLs, headers and all other messages are
discarded before Docker logging. The container log has a 1 MB bound.
Proxy startup is checked locally; a stopped proxy has no direct-route fallback.
The diagnostic filter also emits only validated bare CONNECT hostnames and
numeric ports. Raw request lines, URLs and headers never reach Docker logs.
The live harness captures these bounded diagnostics before cleanup; ALLOW/DENY
describes the configured CONNECT policy, not successful destination connectivity.
Rebuild the proxy image after changing the diagnostic filter.

The auth file travels from `AuthorizedCodexHome` directly to container stdin,
never through a host staging copy, argv or environment. Its containing tmpfs
has mode 0700 and auth.json has mode 0600. Codex uses that directory as
`CODEX_HOME`, also containing its ephemeral state. Version 0.159.2's official
`codex-rs/login/src/auth/storage.rs` confirms auth.json is read from CODEX_HOME;
no additional credential files are invented. No API-key/token-login fallback.
The authenticated executor verifies `account/read` reports ChatGPT after the
existing initialize handshake and before thread/turn. Account metadata is not
returned to application/domain.

Local Docker integration opt-in (synthetic auth, no provider call or Internet):

```sh
DEV_ORCHESTRATOR_P6_DOCKER_TEST=1 go test ./internal/infrastructure -run '^TestCodexEgressDockerOptIn$' -count=1 -v
```

Provider live opt-in requires all three explicitly set variables:
`DEV_ORCHESTRATOR_CODEX_AUTH_LIVE=1`,
`DEV_ORCHESTRATOR_CODEX_AUTH_HOME=<authorized absolute directory>`,
`DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS=<comma-separated exact hostnames>`.
Then run only `TestAuthenticatedCodexLiveOptIn`. It uses a new empty temporary
workspace and requests a fixed text reply, with no project task. It never prints
credentials, account metadata or raw responses. Account failures expose only
infrastructure classifications, numeric RPC codes and fixed safe messages;
only the exact message `workspace routing discovery failed` is allowlisted.
Stop on any
failure and return to the Planner; never expand the allowlist automatically.
An observed denied hostname is reported as BLOCKED_LIVE_DESTINATION; the runtime
never authorizes it automatically. Do not declare B-P4-001 resolved from
initialize/account success alone.

Validation in this session: Linux unit suites plus local Linux Docker policy,
topology, synthetic tmpfs, initialize, unavailable proxy and cleanup checks.
No provider live run: no authorized home/allowlist was supplied for that run.
Official Linux/Docker acceptance: infrastructure, internal and full suites,
vet, build, internal race, and scripts/validate.sh passed without live opt-in.
The log-filter tests also passed during image build and separately in Linux.
The Docker integration test runner alone uses the local daemon socket; neither
Codex nor Tinyproxy receives it. A Windows Application Control execution block
was handled by validating in Linux, without changing security policy.
