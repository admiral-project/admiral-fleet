# admiral-fleet

`admiral-fleet` es el worker de Admiral.

Hace:

- ejecuta tareas autorizadas del control plane.
- provisiona, inicia, detiene, pausa y deprovisiona workloads.
- usa Podman rootless, Quadlet y systemd.
- reporta estado y resultados.

No hace:

- tomar decisiones de negocio.
- escribir en el núcleo de datos de `admirald`.
- recibir tareas por la cola duradera PostgreSQL como contrato runtime oficial.

Estado actual:

- el camino de producción es `SystemdPodmanExecutor`.
- el camino de pruebas sigue siendo `SimulatedExecutor`.
- la cola duradera de tareas usa PostgreSQL.
