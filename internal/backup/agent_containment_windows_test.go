//go:build windows

package backup

import (
	"os"
	"os/exec"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNewKillOnCloseJobEnablesRequiredLimit(t *testing.T) {
	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatalf("newKillOnCloseJob() error = %v", err)
	}
	defer windows.CloseHandle(job)

	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var returned uint32
	if err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
		&returned,
	); err != nil {
		t.Fatalf("QueryInformationJobObject() error = %v", err)
	}
	if limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatalf("Job Object flags = %#x, want JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE", limits.BasicLimitInformation.LimitFlags)
	}
	if returned != uint32(unsafe.Sizeof(limits)) {
		t.Fatalf("QueryInformationJobObject() returned %d bytes, want %d", returned, unsafe.Sizeof(limits))
	}
}

func TestAgentContainmentNestsInsideExistingJob(t *testing.T) {
	const helperEnvironment = "DBTERM_TEST_NESTED_AGENT_JOB"
	if os.Getenv(helperEnvironment) == "1" {
		parent, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			t.Fatalf("CreateJobObject(parent) error = %v", err)
		}
		defer windows.CloseHandle(parent)

		process, err := windows.GetCurrentProcess()
		if err != nil {
			t.Fatalf("GetCurrentProcess() error = %v", err)
		}
		if err := windows.AssignProcessToJobObject(parent, process); err != nil {
			t.Fatalf("assign helper to simulated scheduler job: %v", err)
		}
		containment, err := createAgentContainmentJob()
		if err != nil {
			t.Fatalf("create containment job inside simulated scheduler job: %v", err)
		}
		// Do not close containment: JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE would
		// intentionally terminate this helper. Windows closes it at process exit.
		_ = containment
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestAgentContainmentNestsInsideExistingJob$")
	command.Env = append(os.Environ(), helperEnvironment+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("nested Job Object helper failed: %v\n%s", err, output)
	}
}
