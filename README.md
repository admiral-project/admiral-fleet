# admiral-fleet

Worker agent for the Admiral PaaS platform.

`admiral-fleet` runs on workload nodes and executes authorized tasks received from `admirald`. It interacts locally with Podman, systemd, volumes, and node-level resources.

## Responsibilities

- Register with admirald and report node status
- Receive tasks from RabbitMQ
- Create and manage Podman pods (Quadlet-based)
- Create and manage containers and volumes
- Start, stop, pause, and resume application pods
- Execute database backups (PostgreSQL, MySQL, MariaDB)
- Report task success or failure back to admirald

## Quick start

```bash
export ADMIRAL_FLEET_NODE_ID=node_001
export ADMIRAL_SHARED_TOKEN=dev-token
export ADMIRAL_API_URL=https://127.0.0.1:8080
export ADMIRAL_RABBITMQ_URL=amqps://guest:guest@127.0.0.1:5671/admiral
export ADMIRAL_FLEET_EXECUTOR=systemd-podman
export ADMIRAL_FLEET_QUADLET_DIR=/etc/containers/systemd/admiral
export ADMIRAL_FLEET_DATA_DIR=/var/lib/admiral

admiral-fleet
```

## Executors

- `systemd-podman` (production) — generates Quadlet `.pod`, `.container`, and `.volume` files, managed via systemd
- `simulated` (development only) — logs actions without executing them

## Requirements

- Podman >= 5 with Quadlet `.pod` support
- systemd as init system
- Fedora, Rocky Linux, RHEL, or compatible Enterprise Linux

## Design

`admiral-fleet` does not make business decisions. It executes authorized tasks and reports results. All policy decisions (billing, quotas, suspension) belong to `admirald`.
