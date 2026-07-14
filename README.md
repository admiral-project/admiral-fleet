# admiral-fleet

Worker agent for the Admiral PaaS platform.

`admiral-fleet` runs on workload nodes and executes authorized tasks received from `admirald` via HTTP API. It interacts locally with Podman, systemd, volumes, and node-level resources.

## Responsibilities

- Register with admirald and report node status
- Claim and execute tasks from admirald via HTTP API
- Create and manage Podman pods (Quadlet-based)
- Create and manage containers and volumes
- Start, stop, pause, and resume application pods
- Execute database backups (PostgreSQL, MySQL, MariaDB)
- Report task success or failure back to admirald

## Quick start

```bash
export ADMIRAL_FLEET_NODE_ID=node_001
export ADMIRAL_FLEET_TOKEN=dev-token
export ADMIRAL_API_URL=https://127.0.0.1:8080
export ADMIRAL_FLEET_ROOTLESS_USER=admiral
export ADMIRAL_FLEET_EXECUTOR=systemd-podman
export ADMIRAL_FLEET_QUADLET_DIR=/etc/containers/systemd/admiral
export ADMIRAL_FLEET_DATA_DIR=/var/lib/admiral

admiral-fleet
```

## Configuration

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `ADMIRAL_FLEET_NODE_ID` | Unique identifier for this node | Yes | - |
| `ADMIRAL_FLEET_TOKEN` | Token for authentication with `admirald` | Yes | - |
| `ADMIRAL_API_URL` | URL of the Admiral API | No | `https://127.0.0.1:8080` |
| `ADMIRAL_API_CA_FILE` | Path to CA certificate for API verification | No | - |
| `ADMIRAL_FLEET_ROOTLESS_USER` | Local user for rootless Podman execution | Yes | - |
| `ADMIRAL_FLEET_EXECUTOR` | Task executor implementation (`systemd-podman`, `simulated`) | No | `simulated` |
| `ADMIRAL_FLEET_QUADLET_DIR` | Directory for generated Quadlet files | No | `/etc/containers/systemd/admiral` |
| `ADMIRAL_FLEET_DATA_DIR` | Directory for local data and volumes | No | `/var/lib/admiral` |
| `ADMIRAL_FLEET_HTTP_ADDR` | Listen address for internal HTTP server | No | `127.0.0.1:9099` |
| `ADMIRAL_FLEET_PUBLIC_HOST` | Public hostname or IP of this node | No | - |
| `ADMIRAL_FLEET_PUBLIC_PORT` | Public port for reaching this node | No | - |
| `ADMIRAL_FLEET_STORAGE_CHECK_INTERVAL` | Interval for storage usage checks | No | `60s` |
| `ADMIRAL_FLEET_STORAGE_EXCEEDED_ACTION` | Action when storage is exceeded (`report_only`, `stop_workload`) | No | `report_only` |
| `ADMIRAL_FLEET_CALLBACK_OUTBOX` | Directory for pending callback reports | No | `/var/lib/admiral/outbox` |
| `ADMIRAL_S3_ACCESS_KEY_ID` | S3 access key for backups | No* | - |
| `ADMIRAL_S3_SECRET_ACCESS_KEY` | S3 secret key for backups | No* | - |

\* Required if using remote backup storage.

## Executors

- `systemd-podman` (production) — generates Quadlet `.pod`, `.container`, and `.volume` files, managed via systemd
- `simulated` (development only) — logs actions without executing them

## Rootless Execution & System Integration

`admiral-fleet` is designed for strict rootless operation. To achieve this while maintaining full control over workloads, it interacts with several system components using specific mechanisms.

### System Commands

The agent invokes the following commands on the host:

| Command | Purpose |
|---------|---------|
| `loginctl enable-linger <user>` | Ensures the user session and its systemd manager stay active after logout. |
| `systemctl start systemd-machined` | Ensures the `machined` service is active to support the `--machine` transport. |
| `systemd-run --machine=<user>@ --user` | Executes `systemctl` commands within the rootless user's systemd manager. |
| `runuser ... systemd-run --user ... podman exec` | Executes container commands through the rootless user's D-Bus without the unreliable machine transport. |
| `runuser -u <user> -- env XDG_RUNTIME_DIR=/run/user/<uid> podman` | Executes `podman` commands as the rootless user when systemd-specific cgroup access is not required (e.g., volume inspections). |
| `podman secret` | Manages sensitive data in the Podman internal secret store, which is then injected into containers via Quadlet `Secret=` keys. |

### Quadlet Files

Quadlet files are generated in the directory specified by `ADMIRAL_FLEET_QUADLET_DIR`. By default, if `ADMIRAL_FLEET_ROOTLESS_USER` is set, this path is automatically adjusted to:

`/etc/containers/systemd/users/<UID>/admiral/`

Generated files include:
- `admiral-<instance>.pod`: Defines the Podman pod and shared resource limits (CPU/Memory).
- `admiral-<instance>-<service>.container`: Defines each service container, linked to the pod.
- `admiral-<instance>-<volume>.volume`: Defines named volumes for persistence.

### Permissions & Capabilities

To maintain rootless integrity, the following permissions are enforced:

- **Data Directory (`/var/lib/admiral`)**: Mode `0751`. Subdirectories like `instances` and `backups` use `0700` or `0750` to ensure only the rootless user and the agent can access them.
- **Quadlet Directory**: Mode `0755`. Must be readable by the rootless user's systemd generator.
- **Environment Files**: Mode `0600`. Contains sensitive environment variables for containers.
- **Temporary Env Files**: Mode `0600`, owned by the rootless user under `/var/lib/admiral/tmp`. This shared path remains visible when Fleet uses `PrivateTmp=true`.

### Cgroup Management Hacks

Standard `runuser` or `sudo` do not grant access to the user's systemd cgroup hierarchy. Admiral targets the persistent user manager explicitly:

1. **Lingering**: Enabled via `loginctl` to ensure the user's `systemd --user` instance is always running.
2. **Machine Transport for systemctl**: Commands like `systemctl --user daemon-reload` use `systemd-run --machine=<user>@ --user`.
3. **User bus for Podman exec and secrets**: Fleet exports `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`, then starts a transient `systemd-run --user` unit.
4. **Direct runuser for simple Podman operations**: One-shot `run`, `exists`, `inspect`, and `port` operations use `runuser` with `XDG_RUNTIME_DIR`.

## Requirements

- Podman >= 5 with Quadlet `.pod` support
- systemd as init system
- Rootless execution (mandatory)
- Fedora, Rocky Linux, RHEL, or compatible Enterprise Linux

## Design

`admiral-fleet` does not make business decisions. It executes authorized tasks and reports results. All policy decisions (billing, quotas, suspension) belong to `admirald`.

Tasks are claimed via HTTP API from admirald, not by reading a shared database directly. This ensures fleet never requires write access to the queue database.
