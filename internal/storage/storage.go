package storage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"devd/internal/config"
	"devd/internal/vm"
)

const (
	templateDiskName = "rootfs.ext4"
	manifestName     = "manifest.json"
)

// ImageManifest records immutable OCI and disk-format inputs for a cached
// ext4 template.
type ImageManifest struct {
	Image         string    `json:"image"`
	Digest        string    `json:"digest"`
	Architecture  string    `json:"architecture"`
	DiskMiB       int       `json:"disk_mib"`
	FormatVersion int       `json:"format_version"`
	Environment   []string  `json:"environment,omitempty"`
	WorkingDir    string    `json:"working_dir,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Template is a validated immutable cache entry.
type Template struct {
	Dir      string
	DiskPath string
	Manifest ImageManifest
	Cached   bool
}

type buildahInspect struct {
	FromImage       string `json:"FromImage"`
	FromImageID     string `json:"FromImageID"`
	FromImageDigest string `json:"FromImageDigest"`
	OCIv1           struct {
		Architecture string `json:"architecture"`
		Config       struct {
			Env        []string `json:"Env"`
			WorkingDir string   `json:"WorkingDir"`
		} `json:"config"`
	} `json:"OCIv1"`
}

// CheckDependencies verifies cold image preparation tools. Runtime companions
// are checked separately by vm.CheckRuntime.
func CheckDependencies() error {
	if _, err := exec.LookPath("buildah"); err != nil {
		return fmt.Errorf("buildah not found; run install-runtime")
	}
	if _, err := findE2fsTool("mke2fs"); err != nil {
		return err
	}
	if _, err := findE2fsTool("e2fsck"); err != nil {
		return err
	}
	return nil
}

// EnsureTemplate resolves a local OCI image and prepares one digest-addressed
// immutable ext4 template. Cached templates avoid Buildah container creation.
func EnsureTemplate(image string) (*Template, error) {
	if err := CheckDependencies(); err != nil {
		return nil, err
	}
	canonical := QualifyImage(image)
	inspect, err := ensureLocalImage(canonical)
	if err != nil {
		return nil, err
	}
	digest := inspect.FromImageDigest
	if digest == "" && inspect.FromImageID != "" {
		digest = "sha256:" + strings.TrimPrefix(inspect.FromImageID, "sha256:")
	}
	if digest == "" {
		return nil, fmt.Errorf("inspect image %q: no immutable digest or image ID", canonical)
	}

	imagesDir, err := config.ImagesDir()
	if err != nil {
		return nil, fmt.Errorf("images directory: %w", err)
	}
	key := templateKey(digest)
	finalDir := filepath.Join(imagesDir, key)
	if template, loadErr := loadTemplate(finalDir); loadErr == nil {
		template.Cached = true
		return template, nil
	}

	lock, err := lockTemplate(filepath.Join(imagesDir, "."+key+".lock"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if template, loadErr := loadTemplate(finalDir); loadErr == nil {
		template.Cached = true
		return template, nil
	}
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, fmt.Errorf("remove invalid image template: %w", err)
	}

	tempDir, err := os.MkdirTemp(imagesDir, ".tmp-"+key+"-")
	if err != nil {
		return nil, fmt.Errorf("create template staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	manifest := ImageManifest{
		Image:         canonical,
		Digest:        digest,
		Architecture:  inspect.OCIv1.Architecture,
		DiskMiB:       config.DefaultDiskMiB,
		FormatVersion: config.TemplateFormatVersion,
		Environment:   inspect.OCIv1.Config.Env,
		WorkingDir:    inspect.OCIv1.Config.WorkingDir,
		CreatedAt:     time.Now().UTC(),
	}
	if err := prepareTemplate(tempDir, canonical, manifest); err != nil {
		return nil, err
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return nil, fmt.Errorf("publish image template: %w", err)
	}
	if err := syncDir(imagesDir); err != nil {
		return nil, fmt.Errorf("sync images directory: %w", err)
	}
	return &Template{
		Dir:      finalDir,
		DiskPath: filepath.Join(finalDir, templateDiskName),
		Manifest: manifest,
	}, nil
}

func ensureLocalImage(image string) (*buildahInspect, error) {
	inspect, err := inspectImage(image)
	if err == nil && inspect.OCIv1.Architecture == imageArchitecture() {
		return inspect, nil
	}

	policy, policyErr := signaturePolicy()
	if policyErr != nil {
		return nil, policyErr
	}
	args := []string{"--signature-policy", policy, "pull", "--os", "linux", "--arch", imageArchitecture(), image}
	pull := exec.Command("buildah", args...)
	pull.Stdout = os.Stderr
	pull.Stderr = os.Stderr
	if pullErr := pull.Run(); pullErr != nil {
		return nil, fmt.Errorf("pull image %q: %w", image, pullErr)
	}
	inspect, err = inspectImage(image)
	if err != nil {
		return nil, err
	}
	if inspect.OCIv1.Architecture != imageArchitecture() {
		return nil, fmt.Errorf("image architecture %q does not match host architecture %q", inspect.OCIv1.Architecture, imageArchitecture())
	}
	return inspect, nil
}

func inspectImage(image string) (*buildahInspect, error) {
	output, err := exec.Command("buildah", "inspect", "--type", "image", image).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("inspect image %q: %w\n%s", image, err, output)
	}
	var inspect buildahInspect
	if err := json.Unmarshal(output, &inspect); err != nil {
		return nil, fmt.Errorf("decode image inspection: %w", err)
	}
	return &inspect, nil
}

func prepareTemplate(tempDir, image string, manifest ImageManifest) error {
	policy, err := signaturePolicy()
	if err != nil {
		return err
	}
	args := []string{"--signature-policy", policy, "from", "--os", "linux", "--arch", imageArchitecture(), image}
	output, err := exec.Command("buildah", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create temporary OCI rootfs: %w\n%s", err, output)
	}
	container := strings.TrimSpace(string(output))
	if container == "" {
		return fmt.Errorf("buildah from returned an empty container name")
	}
	defer func() {
		_ = exec.Command("buildah", "umount", container).Run()
		_ = exec.Command("buildah", "rm", container).Run()
	}()

	mountOutput, err := exec.Command("buildah", "mount", container).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount temporary OCI rootfs: %w\n%s", err, mountOutput)
	}
	sourceRoot := strings.TrimSpace(string(mountOutput))
	if sourceRoot == "" {
		return fmt.Errorf("buildah mount returned an empty rootfs path")
	}

	diskPath := filepath.Join(tempDir, templateDiskName)
	if err := createExt4(diskPath, manifest.DiskMiB); err != nil {
		return err
	}
	helperRoot := filepath.Join(tempDir, "helper-root")
	if err := prepareHelperRoot(helperRoot); err != nil {
		return err
	}
	logPath := filepath.Join(tempDir, "conversion.log")
	if err := vm.Pack(vm.PackOpts{
		HelperRoot: helperRoot,
		SourceRoot: sourceRoot,
		TargetDisk: diskPath,
		CPUs:       config.DefaultCPUs,
		Memory:     config.DefaultMemory,
		LogFile:    logPath,
	}); err != nil {
		return fmt.Errorf("populate ext4 template: %w", err)
	}
	if err := os.RemoveAll(helperRoot); err != nil {
		return fmt.Errorf("remove conversion helper root: %w", err)
	}

	e2fsck, err := findE2fsTool("e2fsck")
	if err != nil {
		return err
	}
	if output, err := exec.Command(e2fsck, "-fn", diskPath).CombinedOutput(); err != nil {
		return fmt.Errorf("validate ext4 template: %w\n%s", err, output)
	}
	if err := writeJSON(filepath.Join(tempDir, manifestName), manifest, 0600); err != nil {
		return fmt.Errorf("write image manifest: %w", err)
	}
	if err := syncFile(diskPath); err != nil {
		return fmt.Errorf("sync ext4 template: %w", err)
	}
	return syncDir(tempDir)
}

func prepareHelperRoot(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create conversion helper root: %w", err)
	}
	helper, err := vm.ImageHelperPath()
	if err != nil {
		return err
	}
	if err := copyFile(helper, filepath.Join(dir, "devd-image-helper"), 0755); err != nil {
		return fmt.Errorf("install image helper: %w", err)
	}
	if _, err := vm.WriteGuestInit(dir); err != nil {
		return err
	}
	return nil
}

func createExt4(path string, sizeMiB int) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("create sparse disk: %w", err)
	}
	if err := file.Truncate(int64(sizeMiB) * 1024 * 1024); err != nil {
		_ = file.Close()
		return fmt.Errorf("size sparse disk: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close sparse disk: %w", err)
	}
	mke2fs, err := findE2fsTool("mke2fs")
	if err != nil {
		return err
	}
	if output, err := exec.Command(mke2fs, "-q", "-t", "ext4", "-F", "-m", "0", "-L", "devd-root", path).CombinedOutput(); err != nil {
		return fmt.Errorf("format ext4 template: %w\n%s", err, output)
	}
	return nil
}

// CloneDisk creates an atomic copy-on-write clone. It never silently falls
// back to a full copy because that would reintroduce the create bottleneck.
func CloneDisk(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf("create disk destination directory: %w", err)
	}
	temp := destination + ".tmp"
	_ = os.Remove(temp)
	defer func() { _ = os.Remove(temp) }()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("/bin/cp", "-c", source, temp)
	case "linux":
		cmd = exec.Command("cp", "--reflink=always", "--sparse=always", source, temp)
	default:
		return fmt.Errorf("ext4 disk cloning is unsupported on %s", runtime.GOOS)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone ext4 disk: %w\n%s", err, output)
	}
	if err := os.Rename(temp, destination); err != nil {
		return fmt.Errorf("publish workspace disk: %w", err)
	}
	return syncDir(filepath.Dir(destination))
}

func loadTemplate(dir string) (*Template, error) {
	manifestData, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var manifest ImageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, err
	}
	if manifest.FormatVersion != config.TemplateFormatVersion || manifest.DiskMiB != config.DefaultDiskMiB {
		return nil, fmt.Errorf("template format mismatch")
	}
	disk := filepath.Join(dir, templateDiskName)
	info, err := os.Stat(disk)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(manifest.DiskMiB)*1024*1024 {
		return nil, fmt.Errorf("invalid template disk %s", disk)
	}
	return &Template{Dir: dir, DiskPath: disk, Manifest: manifest}, nil
}

func lockTemplate(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open template lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock image template: %w", err)
	}
	return file, nil
}

func templateKey(digest string) string {
	digest = strings.ReplaceAll(digest, ":", "-")
	return fmt.Sprintf("%s-%s-v%d-%dm", digest, imageArchitecture(), config.TemplateFormatVersion, config.DefaultDiskMiB)
}

// QualifyImage applies Docker Hub semantics without relying on a host-wide
// unqualified-search-registries setting.
func QualifyImage(image string) string {
	if strings.Contains(image, "://") || strings.HasPrefix(image, "containers-storage:") || strings.HasPrefix(image, "docker-archive:") || strings.HasPrefix(image, "oci-archive:") || strings.HasPrefix(image, "dir:") {
		return image
	}
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 1 {
		return "docker.io/library/" + image
	}
	registry := parts[0]
	if strings.Contains(registry, ".") || strings.Contains(registry, ":") || registry == "localhost" {
		return image
	}
	return "docker.io/" + image
}

func imageArchitecture() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	default:
		return "arm64"
	}
}

func signaturePolicy() (string, error) {
	if path := os.Getenv("SIGNATURE_POLICY"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("SIGNATURE_POLICY points to unavailable file %q", path)
	}
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), ".config/containers/policy.json"),
		"/opt/homebrew/etc/containers/policy.json",
		"/usr/local/etc/containers/policy.json",
		"/etc/containers/policy.json",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("containers policy.json not found; set SIGNATURE_POLICY")
}

func findE2fsTool(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	candidates := []string{
		filepath.Join("/opt/homebrew/opt/e2fsprogs/sbin", name),
		filepath.Join("/opt/homebrew/opt/e2fsprogs/bin", name),
		filepath.Join("/usr/local/opt/e2fsprogs/sbin", name),
		filepath.Join("/usr/local/opt/e2fsprogs/bin", name),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found; install e2fsprogs", name)
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, mode); err != nil {
		return err
	}
	if err := syncFile(temp); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
