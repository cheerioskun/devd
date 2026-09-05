package cli

import (
	"reflect"
	"testing"

	"devd/internal/workspace"
)

func TestForkOverrideSemantics(t *testing.T) {
	source := workspacePlan{
		Spec: workspace.Spec{KernelPath: "/kernel", UserCommand: "serve", Environment: []string{"PATH=/bin"}},
		CPUs: 4, Memory: 1024, Ports: []int{8080}, Mount: "/project:/workspace",
	}
	for _, test := range []struct {
		name      string
		overrides forkOverrides
		want      workspacePlan
	}{
		{"inherit", forkOverrides{CPUs: 2, Memory: 512}, source},
		{"clear", forkOverrides{KernelPathChanged: true, UserCommandChanged: true, MountChanged: true, PortsChanged: true},
			workspacePlan{Spec: workspace.Spec{Environment: []string{"PATH=/bin"}}, CPUs: 4, Memory: 1024}},
		{"override", forkOverrides{KernelPath: "/new-kernel", KernelPathChanged: true, CPUs: 8, CPUsChanged: true},
			workspacePlan{Spec: workspace.Spec{KernelPath: "/new-kernel", UserCommand: "serve", Environment: []string{"PATH=/bin"}}, CPUs: 8, Memory: 1024, Ports: []int{8080}, Mount: "/project:/workspace"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := applyForkOverrides(source, test.overrides)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resolved = %#v, want %#v", got, test.want)
			}
		})
	}
	child := applyForkOverrides(source, forkOverrides{})
	child.Ports[0] = 9090
	child.Spec.Environment[0] = "PATH=/elsewhere"
	if source.Ports[0] != 8080 || source.Spec.Environment[0] != "PATH=/bin" {
		t.Fatal("child aliases source slices")
	}
}

func TestEnvironmentLimitsAreExplicit(t *testing.T) {
	values := make([]string, 62)
	for i := range values {
		values[i] = "VALUE=present"
	}
	if _, err := imageEnvironment(values); err == nil {
		t.Fatal("oversize environment was silently truncated")
	}
	got, err := imageEnvironment([]string{"HOME=/elsewhere", "DEVD_NAME=wrong", "DEVD_SSH_PORT=1", "USER=value"})
	if err != nil || !reflect.DeepEqual(got, []string{"USER=value"}) {
		t.Fatalf("environment = %v, %v", got, err)
	}
	for _, value := range []string{"=empty-key", "missing-equals", "KEY=bad\x00value"} {
		if _, err := imageEnvironment([]string{value}); err == nil {
			t.Fatalf("accepted invalid environment %q", value)
		}
	}
}
