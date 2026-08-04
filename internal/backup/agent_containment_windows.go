//go:build windows

package backup

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	agentContainmentOnce sync.Once
	agentContainmentJob  windows.Handle
	agentContainmentErr  error
)

// enableAgentProcessContainment places the agent itself in a private Job
// Object before it can launch a database client. Job membership is inherited
// atomically by every child and descendant. If Task Scheduler hard-terminates
// dbterm, Windows closes dbterm's sole job handle and terminates the entire
// native-client process tree.
func enableAgentProcessContainment() error {
	agentContainmentOnce.Do(func() {
		agentContainmentJob, agentContainmentErr = createAgentContainmentJob()
	})
	if agentContainmentErr != nil {
		return fmt.Errorf("start Windows backup agent safely: %w; scheduled backups were not started because native database clients could otherwise survive a forced agent stop", agentContainmentErr)
	}
	return nil
}

func createAgentContainmentJob() (windows.Handle, error) {
	job, err := newKillOnCloseJob()
	if err != nil {
		return 0, err
	}

	process, err := windows.GetCurrentProcess()
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("get dbterm agent process handle: %w", err)
	}
	// dbterm's Go 1.26 toolchain already requires Windows 10 or Windows
	// Server 2016. Those releases support nested jobs, so a task that Task
	// Scheduler already placed in a compatible job can join this empty child
	// job. We deliberately set no UI restrictions because Windows rejects a
	// nested job hierarchy containing them.
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("assign dbterm agent to its kill-on-close Job Object: %w (requires Windows 10/Server 2016 or newer and a host job that permits compatible nesting)", err)
	}
	return job, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create kill-on-close Job Object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("enable kill-on-close Job Object policy: %w", err)
	}
	return job, nil
}
