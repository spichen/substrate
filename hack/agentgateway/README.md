# Startup egress for the pinned gateway

The pinned gateway accepts actor tunnels only in RUNNING. A workload that needs
network access before its wakeup probe succeeds is still RESUMING, creating a
startup deadlock. This patch permits RESUMING only when a worker IP is assigned.
All other inactive or unknown states remain denied. Actor mTLS authentication,
identity validation, and destination/credential policies remain unchanged.

Dispatch `publish-ghcr` with `gateway_only=true` to test and build the pinned
AgentGateway source plus this patch on a native ARM64 runner. The image is for
the ARM64 cluster; it is not a multi-architecture release. Its tag includes this
repository's commit, while the upstream source is pinned to a full commit SHA.
