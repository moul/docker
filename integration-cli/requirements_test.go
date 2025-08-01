package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/plugins"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/moby/moby/v2/integration-cli/cli"
	"github.com/moby/moby/v2/testutil/registry"
)

func DaemonIsWindows() bool {
	return testEnv.DaemonInfo.OSType == "windows"
}

func DaemonIsLinux() bool {
	return testEnv.DaemonInfo.OSType == "linux"
}

func OnlyDefaultNetworks(ctx context.Context) bool {
	apiClient, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return false
	}
	networks, err := apiClient.NetworkList(ctx, network.ListOptions{})
	if err != nil || len(networks) > 0 {
		return false
	}
	return true
}

func IsAmd64() bool {
	return testEnv.DaemonVersion.Arch == "amd64"
}

func NotPpc64le() bool {
	return testEnv.DaemonVersion.Arch != "ppc64le"
}

func UnixCli() bool {
	return isUnixCli
}

func GitHubActions() bool {
	return os.Getenv("GITHUB_ACTIONS") != ""
}

func Network() bool {
	// Set a timeout on the GET at 15s
	const timeout = 15 * time.Second
	const url = "https://hub.docker.com"

	c := http.Client{
		Timeout: timeout,
	}

	resp, err := c.Get(url)
	if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
		panic(fmt.Sprintf("Timeout for GET request on %s", url))
	}
	if resp != nil {
		resp.Body.Close()
	}
	return err == nil
}

func Apparmor() bool {
	buf, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	return err == nil && len(buf) > 1 && buf[0] == 'Y'
}

// containerdSnapshotterEnabled checks if the daemon in the test-environment is
// configured with containerd-snapshotters enabled.
func containerdSnapshotterEnabled() bool {
	for _, v := range testEnv.DaemonInfo.DriverStatus {
		if v[0] == "driver-type" {
			return v[1] == string(plugins.SnapshotPlugin)
		}
	}
	return false
}

func UserNamespaceROMount() bool {
	// quick case--userns not enabled in this test run
	if os.Getenv("DOCKER_REMAP_ROOT") == "" {
		return true
	}
	if _, _, err := dockerCmdWithError("run", "--rm", "--read-only", "busybox", "date"); err != nil {
		return false
	}
	return true
}

func NotUserNamespace() bool {
	root := os.Getenv("DOCKER_REMAP_ROOT")
	return root == ""
}

func UserNamespaceInKernel() bool {
	if _, err := os.Stat("/proc/self/uid_map"); os.IsNotExist(err) {
		/*
		 * This kernel-provided file only exists if user namespaces are
		 * supported
		 */
		return false
	}

	// We need extra check on redhat based distributions
	if f, err := os.Open("/sys/module/user_namespace/parameters/enable"); err == nil {
		defer f.Close()
		b := make([]byte, 1)
		_, _ = f.Read(b)
		return string(b) != "N"
	}

	return true
}

func IsPausable() bool {
	if testEnv.DaemonInfo.OSType == "windows" {
		return testEnv.DaemonInfo.Isolation.IsHyperV()
	}
	return true
}

// RegistryHosting returns whether the host can host a registry (v2) or not
func RegistryHosting() bool {
	// for now registry binary is built only if we're running inside
	// container through `make test`. Figure that out by testing if
	// registry binary is in PATH.
	_, err := exec.LookPath(registry.V2binary)
	return err == nil
}

func RuntimeIsWindowsContainerd() bool {
	return os.Getenv("DOCKER_WINDOWS_CONTAINERD_RUNTIME") == "1"
}

func SwarmInactive() bool {
	return testEnv.DaemonInfo.Swarm.LocalNodeState == swarm.LocalNodeStateInactive
}

func TODOBuildkit() bool {
	return os.Getenv("DOCKER_BUILDKIT") == ""
}

func DockerCLIVersion(t testing.TB) string {
	out := cli.DockerCmd(t, "--version").Stdout()
	version := strings.Fields(out)
	if len(version) < 3 {
		t.Fatal("unknown version output", version)
	}
	return version[2]
}

// testRequires checks if the environment satisfies the requirements
// for the test to run or skips the tests.
func testRequires(t *testing.T, requirements ...func() bool) {
	t.Helper()
	for _, check := range requirements {
		if !check() {
			requirementFunc := runtime.FuncForPC(reflect.ValueOf(check).Pointer()).Name()
			_, req, _ := strings.Cut(path.Base(requirementFunc), ".")
			t.Skipf("unmatched requirement %s", req)
		}
	}
}
