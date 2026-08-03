// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) inspect(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	snapshot, err := e.inspectSnapshot(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("marshal inspect snapshot for instance %q: %v", task.InstanceID, err)
		return result
	}
	result.Success = true
	result.Logs = fmt.Sprintf("inspected instance %s", task.InstanceID)
	result.Metadata = string(payload)
	return result
}

func (e *SystemdPodmanExecutor) inspectSnapshot(ctx context.Context, task admiral.FleetTask) (map[string]interface{}, error) {
	services := make([]map[string]interface{}, 0, len(task.Services))
	for _, svc := range task.Services {
		containerName := containerName(task.InstanceID, svc.Name)
		containerInspect, err := e.podman().ContainerInspect(ctx, containerName)
		if err != nil {
			return nil, fmt.Errorf("inspect container %q: %w", containerName, err)
		}

		unitName := quadlet.ContainerUnitName(task.InstanceID, svc.Name)
		unitStatus, _ := e.systemd().Status(ctx, unitName)

		serviceSnapshot := map[string]interface{}{
			"name":              svc.Name,
			"image":             svc.Image,
			"container":         containerName,
			"container_unit":    unitName,
			"container_status":  strings.TrimSpace(string(unitStatus)),
			"container_inspect": sanitizedInspectJSONValue(containerInspect),
		}
		if svc.Volume != "" {
			volName := volumeName(task.InstanceID, svc.Name)
			volumeInspect, err := e.podman().VolumeInspect(ctx, volName)
			if err != nil {
				return nil, fmt.Errorf("inspect volume %q: %w", volName, err)
			}
			serviceSnapshot["volume"] = map[string]interface{}{
				"name":    volName,
				"source":  svc.Volume,
				"inspect": sanitizedInspectJSONValue(volumeInspect),
			}
		}
		services = append(services, serviceSnapshot)
	}

	containers, _ := e.podman().ContainerPS(ctx)

	return map[string]interface{}{
		"executor":       "systemd-podman",
		"instance_id":    task.InstanceID,
		"containers":     services,
		"all_containers": sanitizedInspectJSONValue(containers),
		"inspected_at":   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func mustJSONValue(data []byte) interface{} {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	return v
}

func sanitizedInspectJSONValue(data []byte) interface{} {
	value := mustJSONValue(data)
	redactInspectEnvironment(value)
	return value
}

func redactInspectEnvironment(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if strings.EqualFold(key, "Env") {
				if env, ok := child.([]interface{}); ok {
					for i, entry := range env {
						if text, ok := entry.(string); ok {
							if name, _, found := strings.Cut(text, "="); found {
								env[i] = name + "=[REDACTED]"
							}
						}
					}
					continue
				}
			}
			redactInspectEnvironment(child)
		}
	case []interface{}:
		for _, child := range typed {
			redactInspectEnvironment(child)
		}
	}
}

func (e *SystemdPodmanExecutor) startMetadata(ctx context.Context, task admiral.FleetTask) (string, error) {
	hostPorts := make(map[string]int)
	infraContainer := containerName(task.InstanceID, "infra")
	for _, svc := range task.Services {
		if svc.Port > 0 {
			var hostPort string
			for retry := 0; retry < 10; retry++ {
				p, err := e.podman().PodPort(ctx, infraContainer, fmt.Sprintf("%d/tcp", svc.Port))
				if err == nil {
					hostPort = p
					if hostPort != "" {
						break
					}
				}
				select {
				case <-ctx.Done():
					return "", fmt.Errorf("start metadata cancelled while waiting for pod port: %w", ctx.Err())
				case <-time.After(1 * time.Second):
				}
			}
			if hostPort != "" {
				if p, err := parsePublishedPort(hostPort); err == nil {
					hostPorts[svc.Name] = p
				}
			}
		}
	}
	metadata := map[string]interface{}{
		"executor":   "systemd-podman",
		"action":     "start_app",
		"host_ports": hostPorts,
	}
	if task.VerifyImages {
		images, err := e.startImageEvidence(ctx, task)
		if err != nil {
			return "", err
		}
		metadata["images"] = images
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal start metadata: %w", err)
	}
	return string(data), nil
}

type startImageEvidence struct {
	DefinedImage string `json:"defined_image"`
	ImageRef     string `json:"image_ref"`
	ImageID      string `json:"image_id"`
	ContainerID  string `json:"container_id"`
}

type containerImageInspect struct {
	ID        string `json:"Id"`
	ImageID   string `json:"Image"`
	ImageName string `json:"ImageName"`
	Config    struct {
		Image string `json:"Image"`
	} `json:"Config"`
}

func (e *SystemdPodmanExecutor) startImageEvidence(ctx context.Context, task admiral.FleetTask) (map[string]startImageEvidence, error) {
	evidence := make(map[string]startImageEvidence, len(task.Services))
	for _, svc := range task.Services {
		// Services with a setup_command are transient helpers. They are
		// removed after provisioning and therefore have no container to
		// inspect during a later start. Only verify containers that start
		// as part of the running instance.
		if strings.TrimSpace(svc.SetupCommand) != "" {
			continue
		}
		containerName := containerName(task.InstanceID, svc.Name)
		data, err := e.podman().ContainerInspect(ctx, containerName)
		if err != nil {
			return nil, fmt.Errorf("inspect started container %q for image verification: %w", containerName, err)
		}
		var records []containerImageInspect
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, fmt.Errorf("parse image inspection for service %q: %w", svc.Name, err)
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("image inspection for service %q returned no container", svc.Name)
		}
		record := records[0]
		imageID := strings.TrimSpace(record.ImageID)
		if imageID == "" {
			imageID = strings.TrimSpace(record.ID)
		}
		if isImageIDHex(imageID) {
			imageID = "sha256:" + imageID
		}
		imageRef := strings.TrimSpace(record.ImageName)
		if imageRef == "" {
			imageRef = strings.TrimSpace(record.Config.Image)
		}
		if imageID == "" || imageRef == "" {
			return nil, fmt.Errorf("started service %q did not report image reference and ID", svc.Name)
		}
		if !imageReferencesEqual(imageRef, svc.Image) {
			return nil, fmt.Errorf("started service %q uses image %q, expected %q", svc.Name, imageRef, svc.Image)
		}
		evidence[svc.Name] = startImageEvidence{
			DefinedImage: svc.Image,
			ImageRef:     imageRef,
			ImageID:      imageID,
			ContainerID:  strings.TrimSpace(record.ID),
		}
	}
	return evidence, nil
}

func isImageIDHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func imageReferencesEqual(actual, expected string) bool {
	return canonicalImageReference(actual) != "" && canonicalImageReference(actual) == canonicalImageReference(expected)
}

func canonicalImageReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "@sha256:") {
		return value
	}
	first := value
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		first = value[:slash]
	}
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		if strings.Contains(value, "/") {
			return "docker.io/" + value
		}
		return "docker.io/library/" + value
	}
	return value
}
