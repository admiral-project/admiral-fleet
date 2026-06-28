# admiral-fleet

Worker agent for the Admiral PaaS platform.

`admiral-fleet` runs on workload nodes and executes authorized tasks received from `admirald`. It interacts locally with Podman, systemd, volumes, and node-level resources.

## Responsibilities

- Register with admirald and report node status
- Receive tasks from the PostgreSQL-backed durable queue
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
export ADMIRAL_QUEUE_DATABASE_URL=postgres://queue:password@127.0.0.1:5432/admiral_queue?sslmode=require
export ADMIRAL_TASK_PUBLIC_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
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
| `ADMIRAL_QUEUE_DATABASE_URL` | PostgreSQL URL for the task queue | Yes | - |
| `ADMIRAL_TASK_PUBLIC_KEY` | Hex-encoded Ed25519 public key for task verification | Yes | - |
| `ADMIRAL_TASK_ENCRYPTION_KEY` | Hex-encoded 32-byte key for task decryption | No | (fetched from API if missing) |
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
| `systemd-run --machine=<user>@ --user` | Executes `systemctl` or `podman` commands within the rootless user's systemd manager. This is required for correct cgroup management. |
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
- **Temporary Env Files**: Mode `0600`. Created during `podman exec` operations to avoid leaking secrets in the process list.

### Cgroup Management Hacks

Standard `runuser` or `sudo` do not grant access to the user's systemd cgroup hierarchy. Admiral uses the `--machine` transport of `systemd-run` to bridge this gap:

1. **Lingering**: Enabled via `loginctl` to ensure the user's `systemd --user` instance is always running.
2. **Machine Transport**: Commands like `systemctl --user daemon-reload` are wrapped in `systemd-run --machine=<user>@ --user` so they execute within the correct D-Bus and cgroup context of the rootless user.
3. **XDG_RUNTIME_DIR**: Manually exported when using `runuser` to ensure `podman` can locate the user's rootless storage and Unix sockets.

## Requirements

- Podman >= 5 with Quadlet `.pod` support
- systemd as init system
- Rootless execution (mandatory)
- Fedora, Rocky Linux, RHEL, or compatible Enterprise Linux

## Design

`admiral-fleet` does not make business decisions. It executes authorized tasks and reports results. All policy decisions (billing, quotas, suspension) belong to `admirald`.
