//go:build !windows

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/v2/daemon/internal/lazyregexp"
	libcontainerdtypes "github.com/moby/moby/v2/daemon/internal/libcontainerd/types"
	"github.com/moby/moby/v2/errdefs"
	"github.com/pkg/errors"
)

// NOTE: \\s does not detect unicode whitespaces.
// So we use fieldsASCII instead of strings.Fields in parsePSOutput.
// See https://github.com/moby/moby/pull/24358
var psArgsRegexp = lazyregexp.New("\\s+([^\\s]*)=\\s*(PID[^\\s]*)")

func validatePSArgs(psArgs string) error {
	for _, group := range psArgsRegexp.FindAllStringSubmatch(psArgs, -1) {
		if len(group) >= 3 {
			k := group[1]
			v := group[2]
			if k != "pid" {
				return fmt.Errorf(`specifying "%s=%s" is not allowed`, k, v)
			}
		}
	}
	return nil
}

// fieldsASCII is similar to strings.Fields but only allows ASCII whitespaces
func fieldsASCII(s string) []string {
	fn := func(r rune) bool {
		switch r {
		case '\t', '\n', '\f', '\r', ' ':
			return true
		}
		return false
	}
	return strings.FieldsFunc(s, fn)
}

func appendProcess2ProcList(procList *container.TopResponse, fields []string) {
	// Make sure number of fields equals number of header titles
	// merging "overhanging" fields
	process := fields[:len(procList.Titles)-1]
	process = append(process, strings.Join(fields[len(procList.Titles)-1:], " "))
	procList.Processes = append(procList.Processes, process)
}

func hasPid(procs []uint32, pid int) bool {
	for _, p := range procs {
		if int(p) == pid {
			return true
		}
	}
	return false
}

func parsePSOutput(output []byte, procs []uint32) (*container.TopResponse, error) {
	procList := &container.TopResponse{}

	lines := strings.Split(string(output), "\n")
	procList.Titles = fieldsASCII(lines[0])

	pidIndex := -1
	for i, name := range procList.Titles {
		if name == "PID" {
			pidIndex = i
			break
		}
	}
	if pidIndex == -1 {
		return nil, errors.New("Couldn't find PID field in ps output")
	}

	// loop through the output and extract the PID from each line
	// fixing #30580, be able to display thread line also when "m" option used
	// in "docker top" client command
	preContainedPidFlag := false
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		fields := fieldsASCII(line)

		var (
			p   int
			err error
		)

		if fields[pidIndex] == "-" {
			if preContainedPidFlag {
				appendProcess2ProcList(procList, fields)
			}
			continue
		}
		p, err = strconv.Atoi(fields[pidIndex])
		if err != nil {
			return nil, fmt.Errorf("Unexpected pid '%s': %s", fields[pidIndex], err)
		}

		if hasPid(procs, p) {
			preContainedPidFlag = true
			appendProcess2ProcList(procList, fields)
			continue
		}
		preContainedPidFlag = false
	}
	return procList, nil
}

// psPidsArg converts a slice of PIDs to a string consisting
// of comma-separated list of PIDs prepended by "-q".
// For example, psPidsArg([]uint32{1,2,3}) returns "-q1,2,3".
func psPidsArg(pids []uint32) string {
	b := []byte{'-', 'q'}
	for i, p := range pids {
		b = strconv.AppendUint(b, uint64(p), 10)
		if i < len(pids)-1 {
			b = append(b, ',')
		}
	}
	return string(b)
}

// ContainerTop lists the processes running inside of the given
// container by calling ps with the given args, or with the flags
// "-ef" if no args are given.  An error is returned if the container
// is not found, or is not running, or if there are any problems
// running ps, or parsing the output.
func (daemon *Daemon) ContainerTop(name string, psArgs string) (*container.TopResponse, error) {
	if psArgs == "" {
		psArgs = "-ef"
	}

	if err := validatePSArgs(psArgs); err != nil {
		return nil, err
	}

	ctr, err := daemon.GetContainer(name)
	if err != nil {
		return nil, err
	}

	tsk, err := func() (libcontainerdtypes.Task, error) {
		ctr.Lock()
		defer ctr.Unlock()

		tsk, err := ctr.GetRunningTask()
		if err != nil {
			return nil, err
		}
		if ctr.Restarting {
			return nil, errContainerIsRestarting(ctr.ID)
		}
		return tsk, nil
	}()
	if err != nil {
		return nil, err
	}

	infos, err := tsk.Pids(context.Background())
	if err != nil {
		return nil, err
	}
	procs := make([]uint32, len(infos))
	for i, p := range infos {
		procs[i] = p.Pid
	}

	args := strings.Split(psArgs, " ")
	pids := psPidsArg(procs)
	output, err := exec.Command("ps", append(args, pids)...).Output()
	if err != nil {
		// some ps options (such as f) can't be used together with q,
		// so retry without it
		output, err = exec.Command("ps", args...).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				// first line of stderr shows why ps failed
				line := bytes.SplitN(ee.Stderr, []byte{'\n'}, 2)
				if len(line) > 0 && len(line[0]) > 0 {
					err = errors.New(string(line[0]))
				}
			}
			return nil, errdefs.System(errors.Wrap(err, "ps"))
		}
	}
	procList, err := parsePSOutput(output, procs)
	if err != nil {
		return nil, err
	}
	daemon.LogContainerEvent(ctr, events.ActionTop)
	return procList, nil
}
