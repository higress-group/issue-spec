package testsupport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// bootIDOnce caches the host boot identifier for the lifetime of the process.
var bootIDOnce struct {
	sync.Once
	value string
}

// bootID returns a stable identifier for the current OS boot, used to
// disambiguate PID reuse across reboots. It is best-effort: on Linux it reads
// the kernel boot id, falling back to the kernel boot time; on other platforms
// (or on failure) it returns an empty string, in which case liveness decisions
// fall back to a live-PID probe only.
func bootID() string {
	bootIDOnce.Do(func() {
		bootIDOnce.value = readBootID()
	})
	return bootIDOnce.value
}

func readBootID() string {
	if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "btime" {
				return "btime" + fields[1]
			}
		}
	}
	return ""
}

// newRunID builds a run identifier combining the current PID, the host boot id,
// and random bytes so that a run's liveness can be judged without ambiguity even
// after PID reuse. The random suffix contains no '-' so the boot id (which may
// itself contain dashes) can be recovered by splitting on the first and last
// separators.
func newRunID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return fmt.Sprintf("%d-%s-%s", os.Getpid(), bootID(), hex.EncodeToString(buf[:])), nil
}

// parsedRunID holds the components recovered from a run id string.
type parsedRunID struct {
	pid    int
	bootID string
	rand   string
}

// parseRunID splits a run id of the form "<pid>-<boot-id>-<rand>" back into its
// components. The boot id may contain '-' characters (it is often a UUID), while
// the pid and random suffix do not, so the split uses the first and last
// separators.
func parseRunID(id string) (parsedRunID, bool) {
	first := strings.Index(id, "-")
	last := strings.LastIndex(id, "-")
	if first <= 0 || last <= first {
		return parsedRunID{}, false
	}
	pid, err := strconv.Atoi(id[:first])
	if err != nil {
		return parsedRunID{}, false
	}
	return parsedRunID{
		pid:    pid,
		bootID: id[first+1 : last],
		rand:   id[last+1:],
	}, true
}
