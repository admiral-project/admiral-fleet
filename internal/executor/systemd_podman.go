package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type SystemdPodmanExecutor struct {
	Systemd  *systemd.Manager
	Podman   *podman.Inspector
	Renderer *quadlet.Renderer
}

func NewSystemdPodman(systemdManager *systemd.Manager, podmanInspector *podman.Inspector, quadletDir, dataDir string) *SystemdPodmanExecutor {
	return &SystemdPodmanExecutor{
		Systemd:  systemdManager,
		Podman:   podmanInspector,
		Renderer: quadlet.NewRenderer(quadletDir, dataDir),
	}
}

func (e *SystemdPodmanExecutor) Execute(ctx context.Context, task admiral.FleetTask, nodeID string) admiral.TaskResult {
	result := admiral.TaskResult{
		TaskID:      task.TaskID,
		OperationID: task.OperationID,
		NodeID:      nodeID,
	}

	if task.NodeID != nodeID {
		result.Success = false
		result.Error = fmt.Sprintf("task node_id %q does not match fleet node_id %q", task.NodeID, nodeID)
		return result
	}
	if !isAllowedAction(task.Action) {
		result.Success = false
		result.Error = fmt.Sprintf("unsupported action %q", task.Action)
		return result
	}

	switch task.Action {
	case admiral.ActionProvisionApp:
		return e.provision(ctx, task, result)
	case admiral.ActionStartApp, admiral.ActionResumeApp:
		return e.start(ctx, task, result)
	case admiral.ActionStopApp, admiral.ActionPauseApp:
		return e.stop(ctx, task, result)
	case admiral.ActionDeprovisionApp:
		return e.deprovision(ctx, task, result)
	default:
		result.Success = false
		result.Error = fmt.Sprintf("systemd-podman executor action %q is not implemented yet", task.Action)
		result.Metadata = `{"executor":"systemd-podman"}`
		return result
	}
}

func (e *SystemdPodmanExecutor) provision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if err := validateProvisionTask(task); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	if err := e.renderer().Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("render quadlet for instance %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd units for instance %q: %v", task.InstanceID, err)
		return result
	}
	for _, svc := range quadlet.SortedServices(task.Services) {
		if err := e.systemd().Start(ctx, quadlet.ContainerUnitName(task.InstanceID, svc.Name)); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start service %q for instance %q: %v", svc.Name, task.InstanceID, err)
			return result
		}
	}
	if err := e.podman().PodExists(ctx, quadlet.PodName(task.InstanceID)); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("verify pod %q: %v", quadlet.PodName(task.InstanceID), err)
		return result
	}

	result.Success = true
	result.Logs = fmt.Sprintf("provisioned instance %s with systemd-podman", task.InstanceID)
	result.Metadata = `{"executor":"systemd-podman"}`
	return result
}

func (e *SystemdPodmanExecutor) start(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	services := quadlet.SortedServices(task.Services)
	if len(services) == 0 {
		services = []admiral.ServiceInfo{{Name: "app"}}
	}
	for _, svc := range services {
		if err := e.systemd().Start(ctx, quadlet.ContainerUnitName(task.InstanceID, svc.Name)); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start service %q for instance %q: %v", svc.Name, task.InstanceID, err)
			return result
		}
	}
	result.Success = true
	result.Logs = fmt.Sprintf("started instance %s", task.InstanceID)
	result.Metadata = `{"executor":"systemd-podman"}`
	return result
}

func (e *SystemdPodmanExecutor) stop(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	services := quadlet.SortedServices(task.Services)
	if len(services) == 0 {
		services = []admiral.ServiceInfo{{Name: "app"}}
	}
	for i := len(services) - 1; i >= 0; i-- {
		if err := e.systemd().Stop(ctx, quadlet.ContainerUnitName(task.InstanceID, services[i].Name)); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("stop service %q for instance %q: %v", services[i].Name, task.InstanceID, err)
			return result
		}
	}
	result.Success = true
	result.Logs = fmt.Sprintf("stopped instance %s", task.InstanceID)
	result.Metadata = `{"executor":"systemd-podman"}`
	return result
}

func (e *SystemdPodmanExecutor) deprovision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	_ = e.stop(ctx, task, result)
	_ = e.systemd().Stop(ctx, quadlet.PodUnitName(task.InstanceID))
	_ = e.podman().RemovePod(ctx, quadlet.PodName(task.InstanceID))

	if err := e.renderer().Remove(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("remove quadlet files for instance %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd after deprovision %q: %v", task.InstanceID, err)
		return result
	}

	result.Success = true
	result.Logs = fmt.Sprintf("deprovisioned instance %s", task.InstanceID)
	result.Metadata = `{"executor":"systemd-podman"}`
	return result
}

func validateProvisionTask(task admiral.FleetTask) error {
	if strings.TrimSpace(task.InstanceID) == "" {
		return fmt.Errorf("instance_id is required")
	}
	if len(task.Services) == 0 {
		return fmt.Errorf("provision_app requires at least one service")
	}
	for _, svc := range task.Services {
		if strings.TrimSpace(svc.Name) == "" {
			return fmt.Errorf("service name is required")
		}
		if strings.TrimSpace(svc.Image) == "" {
			return fmt.Errorf("service %q image is required", svc.Name)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) systemd() *systemd.Manager {
	if e.Systemd != nil {
		return e.Systemd
	}
	return systemd.NewManager(nil)
}

func (e *SystemdPodmanExecutor) podman() *podman.Inspector {
	if e.Podman != nil {
		return e.Podman
	}
	return podman.NewInspector(nil)
}

func (e *SystemdPodmanExecutor) renderer() *quadlet.Renderer {
	if e.Renderer != nil {
		return e.Renderer
	}
	return quadlet.NewRenderer("", "")
}
