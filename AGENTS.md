admiral-fleet
Definición de producto

admiral-fleet es el agente worker de Admiral. Se instala en los nodos encargados de ejecutar cargas de trabajo y recibe instrucciones desde admirald para crear, iniciar, detener, pausar, respaldar y administrar aplicaciones contenerizadas agrupadas en pods.

admiral-fleet actúa como el execution plane de Admiral.

Su función principal es traducir órdenes del control plane en acciones concretas sobre el sistema operativo, Podman, systemd, volúmenes y servicios locales.

Propósito principal

Ejecutar de forma segura y controlada las acciones técnicas necesarias para operar aplicaciones SaaS en contenedores sobre nodos Linux.

Responsabilidades principales

admiral-fleet debe encargarse de:

Registrarse ante admirald.
Reportar estado del nodo.
Recibir tareas desde RabbitMQ.
Ejecutar tareas autorizadas.
Crear pods con Podman.
Crear contenedores de servicios.
Crear volúmenes persistentes.
Generar unidades systemd para pods o servicios.
Iniciar y detener aplicaciones.
Pausar y reanudar aplicaciones.
Ejecutar backups de bases de datos.
Reportar éxito o fallo de tareas.
Enviar logs relevantes de operación.
Reportar métricas básicas del nodo.
Validar que una tarea corresponde a su nodo.
Protegerse contra instrucciones inválidas o peligrosas.
Lo que admiral-fleet sí debe hacer

admiral-fleet debe:

Ejecutar acciones locales en el nodo.
Integrarse con Podman.
Integrarse con systemd.
Crear y administrar pods.
Administrar volúmenes relacionados con apps.
Ejecutar scripts o comandos controlados definidos por Admiral.
Reportar estado al control plane.
Mantener una cola local básica o estado temporal de ejecución.
Ser idempotente cuando sea posible.
Ser seguro por defecto.
Lo que admiral-fleet no debe hacer

admiral-fleet no debe:

Tomar decisiones de negocio.
Decidir si un cliente pagó o no.
Decidir precios o tiers.
Exponer una UI.
Modificar directamente datos globales de Admiral.
Ejecutar instrucciones arbitrarias no validadas.
Aceptar tareas de cualquier origen.
Descargar o ejecutar imágenes no permitidas si la política de Admiral lo restringe.
Convertirse en un orquestador complejo.
Funciones mínimas del MVP
Registro y salud del nodo
Registrar nodo ante admirald.
Enviar heartbeat.
Reportar versión del agente.
Reportar sistema operativo.
Reportar versión de Podman.
Reportar recursos básicos: CPU, RAM, disco.
Reportar apps/pods en ejecución.
Ejecución de tareas

Tareas mínimas:

provision_app
start_app
stop_app
pause_app
resume_app
backup_database
resize_app
deprovision_app
inspect_app
Operación con Podman

Debe poder:

Crear pod.
Crear contenedores dentro del pod.
Configurar puertos.
Configurar variables de entorno.
Configurar volúmenes.
Descargar imágenes.
Iniciar pod.
Detener pod.
Eliminar pod.
Inspeccionar estado.
Integración con systemd

Debe poder:

Generar unidades systemd para pods.
Habilitar unidades.
Iniciar unidades.
Detener unidades.
Consultar estado.
Reiniciar servicios si aplica.
Backups

Para MVP, debe soportar al menos:

Backup de PostgreSQL en contenedor.
Generación de archivo comprimido.
Registro de metadata.
Entrega del resultado a una ruta controlada.
Reporte del backup a admirald.
Modelo de ejecución

Flujo típico de provisionamiento:

1. admirald crea operación provision_app.
2. admirald publica tarea en RabbitMQ.
3. admiral-fleet recibe tarea.
4. admiral-fleet valida que la tarea corresponde a su nodo.
5. admiral-fleet crea volúmenes.
6. admiral-fleet crea pod.
7. admiral-fleet crea contenedores.
8. admiral-fleet genera unidad systemd.
9. admiral-fleet inicia la app.
10. admiral-fleet inspecciona estado.
11. admiral-fleet reporta éxito o error a admirald.
Estructura conceptual de una tarea

Ejemplo:

{
  "task_id": "task_123",
  "operation_id": "op_456",
  "node_id": "node_001",
  "action": "provision_app",
  "instance_id": "inst_789",
  "app": {
    "name": "simple-crm",
    "version": "1.0.0"
  },
  "tier": {
    "name": "starter",
    "cpu": 1,
    "memory": "1G",
    "storage": "10G"
  },
  "services": [
    {
      "name": "app",
      "image": "registry.example.com/simple-crm:1.0.0",
      "port": 8080
    },
    {
      "name": "db",
      "image": "docker.io/library/postgres:16",
      "volume": "db_data"
    }
  ]
}
Requerimientos técnicos
Lenguaje: Go.
Servicio systemd.
Integración con Podman.
Integración con systemd.
Consumo de tareas desde RabbitMQ.
Comunicación segura con admirald.
Logs estructurados.
Manejo robusto de errores.
Reintentos controlados.
Ejecución idempotente cuando sea posible.
Distribución como RPM.
Configuración por archivo y variables de entorno.
Operación compatible con Fedora y Enterprise Linux.
Criterio de éxito

admiral-fleet será exitoso si puede convertir instrucciones de admirald en acciones reales sobre contenedores y servicios Linux de forma segura, repetible y auditable, manteniendo al nodo en estado controlado sin requerir intervención manual constante.
