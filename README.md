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

## Requirements

- Podman >= 5 with Quadlet `.pod` support
- systemd as init system
- Rootless execution (mandatory)
- Fedora, Rocky Linux, RHEL, or compatible Enterprise Linux

## Design

`admiral-fleet` does not make business decisions. It executes authorized tasks and reports results. All policy decisions (billing, quotas, suspension) belong to `admirald`.
