//go:build windows

package devctl

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	b, err := exec.Command("whoami.exe", "/user", "/fo", "csv", "/nh").Output()
	if err != nil {
		return fmt.Errorf("cannot identify Windows user for private state ACL")
	}
	records, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) != 2 {
		return fmt.Errorf("invalid whoami output")
	}
	sid := strings.TrimSpace(records[0][1])
	if !strings.HasPrefix(sid, "S-1-") {
		return fmt.Errorf("invalid Windows SID")
	}
	if err = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "*"+sid+":(OI)(CI)F").Run(); err != nil {
		return fmt.Errorf("cannot restrict state directory ACL")
	}
	return nil
}
func platformCommand(a []string) (*exec.Cmd, error) {
	lower := strings.ToLower(a[0])
	if !strings.HasSuffix(lower, ".cmd") && !strings.HasSuffix(lower, ".bat") {
		return exec.Command(a[0], a[1:]...), nil
	}
	quoted := make([]string, len(a))
	for i, s := range a {
		if strings.ContainsAny(s, "\"%!&|<>^\r\n") {
			return nil, fmt.Errorf("batch arguments contain cmd.exe metacharacters; use a direct java.exe command")
		}
		quoted[i] = "\"" + s + "\""
	}
	c := exec.Command("cmd.exe")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd.exe /d /s /c \"" + strings.Join(quoted, " ") + "\""}
	return c, nil
}
func setProcessGroup(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags |= 0x00000200
}
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
}

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var createJob = kernel32.NewProc("CreateJobObjectW")
var setJob = kernel32.NewProc("SetInformationJobObject")
var assignJob = kernel32.NewProc("AssignProcessToJobObject")
var terminateJob = kernel32.NewProc("TerminateJobObject")

type basicLimits struct {
	ProcessTime   int64
	JobTime       int64
	Flags         uint32
	MinWorking    uintptr
	MaxWorking    uintptr
	ActiveProcess uint32
	Affinity      uintptr
	Priority      uint32
	Scheduling    uint32
}
type ioCounters struct{ ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes uint64 }
type extendedLimits struct {
	Basic                                          basicLimits
	IO                                             ioCounters
	ProcessMemory, JobMemory, PeakProcess, PeakJob uintptr
}

func ownProcess(c *exec.Cmd) (func(), func(), error) {
	job, _, e := createJob.Call(0, 0)
	if job == 0 {
		return nil, nil, fmt.Errorf("CreateJobObject: %v", e)
	}
	closeJob := func() { _ = syscall.CloseHandle(syscall.Handle(job)) }
	limits := extendedLimits{}
	limits.Basic.Flags = 0x00002000 // KILL_ON_JOB_CLOSE
	ok, _, e := setJob.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		closeJob()
		return nil, nil, fmt.Errorf("SetInformationJobObject: %v", e)
	}
	ph, e := syscall.OpenProcess(0x0100|0x0001, false, uint32(c.Process.Pid))
	if e != nil {
		closeJob()
		return nil, nil, e
	}
	defer syscall.CloseHandle(ph)
	ok, _, e = assignJob.Call(job, uintptr(ph))
	if ok == 0 {
		closeJob()
		return nil, nil, fmt.Errorf("AssignProcessToJobObject: %v", e)
	}
	return func() { terminateJob.Call(job, 1) }, closeJob, nil
}
