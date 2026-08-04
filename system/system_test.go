package system

import (
	"runtime"
	"testing"
)

// The Panel calls /api/system to render a node's system information. On darwin
// there is no Docker daemon by design, and /etc/os-release does not exist, and
// both of those used to fail the whole call with a 500 so a macOS node reported
// nothing at all. Neither absence is an error.
func TestGetSystemInformationSurvivesWithoutDocker(t *testing.T) {
	i, err := GetSystemInformation()
	if err != nil {
		t.Fatalf("system information must be reportable without a Docker daemon: %v", err)
	}
	if i.System.OSType != runtime.GOOS {
		t.Errorf("OSType = %q, want %q", i.System.OSType, runtime.GOOS)
	}
	if i.System.CPUThreads <= 0 {
		t.Errorf("CPUThreads = %d, want a positive count", i.System.CPUThreads)
	}
	if i.System.KernelVersion == "" {
		t.Error("KernelVersion is empty")
	}
	// The fields the daemon would normally supply have to come from the kernel
	// instead, or the Panel shows a node with no memory and no OS.
	if i.System.MemoryBytes <= 0 {
		t.Errorf("MemoryBytes = %d, want the host's installed memory", i.System.MemoryBytes)
	}
	if i.System.OS == "" {
		t.Error("OS is empty")
	}
}
