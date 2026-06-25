// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

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
	HostPorts  map[string]int // service name -> allocated host port
}

// wantedBy returns the systemd target for Quadlet [Install] sections.
// Admiral always runs workloads rootless, so only user-level targets are used.
func (r *Renderer) wantedBy() string {
	return "default.target"
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
	if err := os.MkdirAll(r.QuadletDir, 0750); err != nil {
		return fmt.Errorf("create quadlet dir: %w", err)
	}
	if err := os.Chmod(r.QuadletDir, 0755); err != nil { // #nosec G302 -- Quadlet directory needs to be accessible by the rootless user
		return fmt.Errorf("set quadlet dir mode: %w", err)
	}

	if err := writeFile(filepath.Join(r.QuadletDir, PodFileName(task.InstanceID)), r.renderPod(task), 0644); err != nil {
		return err
	}

	for _, shared := range SortedSharedVolumes(task.SharedVolumes) {
		if err := writeFile(filepath.Join(r.QuadletDir, SharedVolumeFileName(task.InstanceID, shared.Name)), renderSharedVolume(task.InstanceID, shared.Name, r.wantedBy()), 0644); err != nil {
			return err
		}
	}

	for _, svc := range SortedServices(task.Services) {
		if svc.Volume != "" {
			if err := writeFile(filepath.Join(r.QuadletDir, VolumeFileName(task.InstanceID, svc.Name)), renderVolume(task.InstanceID, svc.Name, r.wantedBy()), 0644); err != nil {
				return err
			}
		}
		envPath := filepath.Join(envDir, svc.Name+".env")
		if err := writeFile(envPath, renderEnv(svc), 0600); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(r.QuadletDir, ContainerFileName(task.InstanceID, svc.Name)), r.renderContainer(task.InstanceID, svc, envPath), 0644); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) Remove(instanceID string) error {
	pattern := filepath.Join(r.QuadletDir, "admiral-"+SafeName(instanceID)+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, path := range matches {
		if strings.HasSuffix(path, ".pod") || strings.HasSuffix(path, ".container") || strings.HasSuffix(path, ".volume") || strings.HasSuffix(path, ".network") {
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

func SortedSharedVolumes(volumes []admiral.SharedVolumeInfo) []admiral.SharedVolumeInfo {
	out := append([]admiral.SharedVolumeInfo(nil), volumes...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func PodFileName(instanceID string) string {
	return fmt.Sprintf("admiral-%s.pod", SafeName(instanceID))
}

func ContainerFileName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.container", SafeName(instanceID), SafeName(serviceName))
}

func VolumeFileName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.volume", SafeName(instanceID), SafeName(serviceName))
}

func SharedVolumeFileName(instanceID, volumeName string) string {
	return fmt.Sprintf("admiral-%s-shared-%s.volume", SafeName(instanceID), SafeName(volumeName))
}

func PodUnitName(instanceID string) string {
	return fmt.Sprintf("admiral-%s-pod.service", SafeName(instanceID))
}

func ContainerUnitName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s.service", SafeName(instanceID), SafeName(serviceName))
}

func VolumeUnitName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s-volume.service", SafeName(instanceID), SafeName(serviceName))
}

func SharedVolumeUnitName(instanceID, volumeName string) string {
	return fmt.Sprintf("admiral-%s-shared-%s-volume.service", SafeName(instanceID), SafeName(volumeName))
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

func (r *Renderer) renderPod(task admiral.FleetTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Admiral pod for instance %s\n\n", task.InstanceID)
	fmt.Fprintf(&b, "[Pod]\nPodName=%s\n", podName(task.InstanceID))
	if limit := formatCPULimit(task.Tier.CPU); limit != "" {
		fmt.Fprintf(&b, "PodmanArgs=--cpus=%s\n", limit)
	}
	if limit := formatMemoryLimit(task.Tier.Memory); limit != "" {
		fmt.Fprintf(&b, "PodmanArgs=--memory=%s\n", limit)
	}
	for _, svc := range task.Services {
		if svc.Port > 0 {
			hostPort, ok := r.HostPorts[svc.Name]
			if ok && hostPort > 0 {
				fmt.Fprintf(&b, "PublishPort=%d:%d\n", hostPort, svc.Port)
			} else {
				fmt.Fprintf(&b, "PublishPort=%d\n", svc.Port)
			}
		}
	}
	fmt.Fprintf(&b, "\n[Service]\nRestart=always\nTimeoutStartSec=900\n\n[Install]\nWantedBy=%s\n", r.wantedBy())
	return b.String()
}

func (r *Renderer) renderContainer(instanceID string, svc admiral.ServiceInfo, envPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Admiral service %s for instance %s\n", svc.Name, instanceID)
	for _, dep := range svc.DependsOn {
		unit := ContainerUnitName(instanceID, dep)
		fmt.Fprintf(&b, "Wants=%s\n", unit)
		fmt.Fprintf(&b, "After=%s\n", unit)
	}
	for _, req := range svc.Requires {
		unit := ContainerUnitName(instanceID, req)
		fmt.Fprintf(&b, "Requires=%s\n", unit)
		fmt.Fprintf(&b, "After=%s\n", unit)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "[Container]\nContainerName=%s\nImage=%s\n", containerName(instanceID, svc.Name), sanitizeQuadletValue(svc.Image))
	if svc.Command != "" {
		fmt.Fprintf(&b, "Exec=%s\n", sanitizeQuadletValue(svc.Command))
	}
	fmt.Fprintf(&b, "EnvironmentFile=%s\n", envPath)
	fmt.Fprintf(&b, "Pod=%s\n", PodFileName(instanceID))
	fmt.Fprintf(&b, "CgroupsMode=no-conmon\n")
	if svc.Volume != "" {
		fmt.Fprintf(&b, "Volume=%s:%s\n", VolumeFileName(instanceID, svc.Name), defaultVolumeTarget(svc))
	}
	for _, shared := range svc.SharedVolumes {
		fmt.Fprintf(&b, "Volume=%s:%s\n", SharedVolumeFileName(instanceID, shared.Name), shared.Mount)
	}
	for _, line := range r.renderSecretMounts(instanceID, svc) {
		fmt.Fprint(&b, line)
	}
	fmt.Fprintf(&b, "\n[Service]\nRestart=always\nTimeoutStartSec=900\n\n[Install]\nWantedBy=%s\n", r.wantedBy())
	return b.String()
}

// renderSecretMounts generates Secret= lines for the Quadlet [Container] section.
//
// Secret= is the standard Quadlet approach for injecting sensitive values as
// environment variables into containers. Podman manages these secrets in its
// internal secret store (encrypted at rest).
//
// This replaces the previous LoadCredentialEncrypted approach, which was never
// a valid Quadlet key. systemd >=256 quadlet-generator rejects unknown keys in
// [Container] with "unsupported key" error, preventing unit generation entirely.
// Moving LoadCredentialEncrypted to [Service] avoided the unit error but broke
// credential delivery: systemd handled the decryption but Podman never received
// the credential, causing containers to fail with status 243/CREDENTIALS.
//
// With Secret=, Quadlet translates the directive to --secret in the generated
// Podman command, so credentials arrive correctly in the container.
//
// Secrets must be created with 'podman secret create' before the Quadlet units
// are started, and cleaned up on deprovision.
func (r *Renderer) renderSecretMounts(instanceID string, svc admiral.ServiceInfo) []string {
	keys := make([]string, 0, len(svc.Secrets))
	for k := range svc.Secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("Secret=%s,type=env,target=%s\n", SecretName(instanceID, svc.Name, k), k))
	}
	return lines
}

// SecretName returns the Podman secret name for a given instance/service/key combination.
// The name must be unique within the Podman secret store for the rootless user.
func SecretName(instanceID, serviceName, key string) string {
	return fmt.Sprintf("admiral-%s-%s-%s", SafeName(instanceID), SafeName(serviceName), SafeName(key))
}

func formatCPULimit(cpu float64) string {
	if cpu <= 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", cpu), "0"), ".")
}

func formatMemoryLimit(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	lower := strings.ToLower(value)
	switch {
	case strings.HasSuffix(lower, "mib"):
		return value[:len(value)-3] + "m"
	case strings.HasSuffix(lower, "mi"):
		return value[:len(value)-2] + "m"
	case strings.HasSuffix(lower, "mb"):
		return value[:len(value)-2] + "m"
	case strings.HasSuffix(lower, "gib"):
		return value[:len(value)-3] + "g"
	case strings.HasSuffix(lower, "gi"):
		return value[:len(value)-2] + "g"
	case strings.HasSuffix(lower, "gb"):
		return value[:len(value)-2] + "g"
	}
	return value
}

func renderVolume(instanceID, serviceName, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Volume]\nVolumeName=%s\n", volumeName(instanceID, serviceName))
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.String()
}

func renderSharedVolume(instanceID, volumeName, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Volume]\nVolumeName=%s\n", sharedVolumeName(instanceID, volumeName))
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.String()
}

func renderEnv(svc admiral.ServiceInfo) string {
	keys := make([]string, 0, len(svc.Env))
	values := make(map[string]string, len(svc.Env))
	for k, v := range svc.Env {
		keys = append(keys, k)
		values[k] = v
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, sanitizeEnvValue(values[k]))
	}
	return b.String()
}

func sanitizeQuadletValue(value string) string {
	// Remove null bytes and replace line breaks with spaces
	value = strings.NewReplacer("\n", " ", "\r", " ", "\x00", "").Replace(value)
	// Whitelist printable ASCII characters excluding backslash, backtick, and dollar
	var b strings.Builder
	for _, r := range value {
		if r >= ' ' && r <= '~' && r != '\\' && r != '`' && r != '$' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func sanitizeEnvValue(value string) string {
	value = strings.NewReplacer(
		"\n", "\\n",
		"\r", "\\r",
		"\x00", "",
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
	).Replace(value)
	return value
}

func podName(instanceID string) string {
	return fmt.Sprintf("admiral-%s", SafeName(instanceID))
}

func containerName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", SafeName(instanceID), SafeName(serviceName))
}

func volumeName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", SafeName(instanceID), SafeName(serviceName))
}

func sharedVolumeName(instanceID, volumeName string) string {
	return fmt.Sprintf("admiral-%s-shared-%s", SafeName(instanceID), SafeName(volumeName))
}

func defaultVolumeTarget(svc admiral.ServiceInfo) string {
	img := strings.ToLower(svc.Image)
	if strings.Contains(img, "postgres") {
		return "/var/lib/postgresql/data"
	}
	if strings.Contains(img, "mariadb") || strings.Contains(img, "mysql") {
		return "/var/lib/mysql"
	}
	if strings.Contains(img, "wordpress") {
		return "/var/www/html/wp-content"
	}
	if svc.Name == "db" {
		return "/var/lib/postgresql/data"
	}
	return "/data"
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
