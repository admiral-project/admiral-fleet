package quadlet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type Renderer struct {
	QuadletDir string
	DataDir    string
}

func NewRenderer(quadletDir, dataDir string) *Renderer {
	return &Renderer{
		QuadletDir: defaultString(quadletDir, "/etc/containers/systemd"),
		DataDir:    defaultString(dataDir, "/var/lib/admiral"),
	}
}

func (r *Renderer) Render(task admiral.FleetTask) error {
	instanceDir := filepath.Join(r.DataDir, "instances", task.InstanceID)
	envDir := filepath.Join(instanceDir, "env")
	if err := os.MkdirAll(envDir, 0700); err != nil {
		return fmt.Errorf("create env dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(instanceDir, "data"), 0750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(r.QuadletDir, 0755); err != nil {
		return fmt.Errorf("create quadlet dir: %w", err)
	}

	if err := writeFile(filepath.Join(r.QuadletDir, PodFileName(task.InstanceID)), renderPod(task), 0644); err != nil {
		return err
	}

	for _, svc := range SortedServices(task.Services) {
		if svc.Volume != "" {
			if err := writeFile(filepath.Join(r.QuadletDir, VolumeFileName(task.InstanceID, svc.Name)), renderVolume(task.InstanceID, svc.Name), 0644); err != nil {
				return err
			}
		}
		envPath := filepath.Join(envDir, svc.Name+".env")
		if err := writeFile(envPath, renderEnv(svc), 0600); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(r.QuadletDir, ContainerFileName(task.InstanceID, svc.Name)), renderContainer(task.InstanceID, svc, envPath), 0644); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) Remove(instanceID string) error {
	pattern := filepath.Join(r.QuadletDir, "admiral-"+SafeName(instanceID)+"-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, path := range matches {
		if strings.HasSuffix(path, ".pod") || strings.HasSuffix(path, ".container") || strings.HasSuffix(path, ".volume") {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func SortedServices(services []admiral.ServiceInfo) []admiral.ServiceInfo {
	out := append([]admiral.ServiceInfo(nil), services...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == "db" {
			return true
		}
		if out[j].Name == "db" {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func PodFileName(instanceID string) string {
	return fmt.Sprintf("admiral-%s-pod.pod", SafeName(instanceID))
}

func ContainerFileName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.container", SafeName(instanceID), SafeName(serviceName))
}

func VolumeFileName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.volume", SafeName(instanceID), SafeName(serviceName))
}

func PodUnitName(instanceID string) string {
	return fmt.Sprintf("admiral-%s-pod-pod.service", SafeName(instanceID))
}

func ContainerUnitName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.service", SafeName(instanceID), SafeName(serviceName))
}

func PodName(instanceID string) string {
	return fmt.Sprintf("admiral-%s", SafeName(instanceID))
}

func SafeName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('-')
	}
	return b.String()
}

func renderPod(task admiral.FleetTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Admiral instance %s pod\n\n", task.InstanceID)
	fmt.Fprintf(&b, "[Pod]\nPodName=%s\n", PodName(task.InstanceID))
	for _, svc := range SortedServices(task.Services) {
		if svc.Port > 0 {
			fmt.Fprintf(&b, "PublishPort=127.0.0.1::%d\n", svc.Port)
		}
	}
	b.WriteString("\n[Service]\nTimeoutStartSec=900\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

func renderContainer(instanceID string, svc admiral.ServiceInfo, envPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Admiral service %s for instance %s\n\n", svc.Name, instanceID)
	fmt.Fprintf(&b, "[Container]\nContainerName=%s\nImage=%s\nPod=%s\nEnvironmentFile=%s\n", containerName(instanceID, svc.Name), svc.Image, PodFileName(instanceID), envPath)
	if svc.Volume != "" {
		fmt.Fprintf(&b, "Volume=%s:/%s\n", VolumeFileName(instanceID, svc.Name), defaultVolumeTarget(svc))
	}
	b.WriteString("\n[Service]\nRestart=always\nTimeoutStartSec=900\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

func renderVolume(instanceID, serviceName string) string {
	return fmt.Sprintf("[Volume]\nVolumeName=%s\n\n[Install]\nWantedBy=multi-user.target\n", volumeName(instanceID, serviceName))
}

func renderEnv(svc admiral.ServiceInfo) string {
	keys := make([]string, 0, len(svc.Env)+len(svc.Secrets))
	values := make(map[string]string, len(svc.Env)+len(svc.Secrets))
	for k, v := range svc.Env {
		keys = append(keys, k)
		values[k] = v
	}
	for k, v := range svc.Secrets {
		keys = append(keys, k)
		values[k] = v
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	return b.String()
}

func containerName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", SafeName(instanceID), SafeName(serviceName))
}

func volumeName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", SafeName(instanceID), SafeName(serviceName))
}

func defaultVolumeTarget(svc admiral.ServiceInfo) string {
	if svc.Name == "db" || strings.Contains(svc.Image, "postgres") {
		return "var/lib/postgresql/data"
	}
	return "data"
}

func writeFile(path, content string, perm os.FileMode) error {
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
