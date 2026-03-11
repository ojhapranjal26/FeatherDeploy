//go:build linux

package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
)

func collectStats() heartbeat.BrainStats {
	var st heartbeat.BrainStats
	st.RAMUsed, st.RAMTotal = readMemInfo()
	st.DiskUsed, st.DiskTotal = diskStatfs("/")
	st.CPU = readCPUPercent()
	return st
}

func readMemInfo() (used, total int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	var memTotal, memAvail int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 2 {
			continue
		}
		val, _ := strconv.ParseInt(parts[1], 10, 64)
		val *= 1024 // kB → bytes
		switch parts[0] {
		case "MemTotal:":
			memTotal = val
		case "MemAvailable:":
			memAvail = val
		}
	}
	return memTotal - memAvail, memTotal
}

func diskStatfs(path string) (used, total int64) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return
	}
	total = int64(s.Blocks) * int64(s.Bsize)
	free := int64(s.Bavail) * int64(s.Bsize)
	return total - free, total
}

func readCPUPercent() float64 {
	read := func() (idle, total int64) {
		f, err := os.Open("/proc/stat")
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "cpu ") {
				continue
			}
			fields := strings.Fields(line)
			for i, v := range fields[1:] {
				n, _ := strconv.ParseInt(v, 10, 64)
				total += n
				if i == 3 {
					idle = n
				}
			}
			return
		}
		return
	}
	idle1, total1 := read()
	time.Sleep(200 * time.Millisecond)
	idle2, total2 := read()
	if total2 == total1 {
		return 0
	}
	return 100.0 * float64(total2-idle2-(total1-idle1)) / float64(total2-total1)
}

