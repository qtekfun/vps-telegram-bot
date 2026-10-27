// Package hoststat reads host memory, load, uptime and disk usage. Inside a
// container /proc/meminfo, /proc/loadavg and /proc/uptime show host values.
package hoststat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Disk struct {
	Path        string
	Total, Free uint64 // bytes
	Err         error
}

type Info struct {
	MemTotal, MemAvailable uint64 // bytes
	SwapTotal, SwapFree    uint64 // bytes
	Load1, Load5, Load15   float64
	Uptime                 time.Duration
	Disks                  []Disk
	Errs                   []string
}

// Read gathers everything it can; failures are collected in Errs.
func Read(procDir string, diskPaths []string) Info {
	var in Info
	if err := readMeminfo(filepath.Join(procDir, "meminfo"), &in); err != nil {
		in.Errs = append(in.Errs, "meminfo: "+err.Error())
	}
	if err := readLoad(filepath.Join(procDir, "loadavg"), &in); err != nil {
		in.Errs = append(in.Errs, "loadavg: "+err.Error())
	}
	if err := readUptime(filepath.Join(procDir, "uptime"), &in); err != nil {
		in.Errs = append(in.Errs, "uptime: "+err.Error())
	}
	for _, p := range diskPaths {
		in.Disks = append(in.Disks, readDisk(p))
	}
	return in
}

func readMeminfo(path string, in *Info) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	found := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			in.MemTotal, found = kb*1024, found+1
		case "MemAvailable":
			in.MemAvailable, found = kb*1024, found+1
		case "SwapTotal":
			in.SwapTotal = kb * 1024
		case "SwapFree":
			in.SwapFree = kb * 1024
		}
	}
	if found < 2 {
		return errors.New("MemTotal/MemAvailable not found")
	}
	return sc.Err()
}

func readLoad(path string, in *Info) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return errors.New("unexpected format")
	}
	for i, dst := range []*float64{&in.Load1, &in.Load5, &in.Load15} {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return err
		}
		*dst = v
	}
	return nil
}

func readUptime(path string, in *Info) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return errors.New("unexpected format")
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return err
	}
	in.Uptime = time.Duration(secs * float64(time.Second))
	return nil
}

func readDisk(path string) Disk {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{Path: path, Err: err}
	}
	bs := uint64(max(st.Bsize, 0)) //nolint:gosec // clamped above zero
	return Disk{Path: path, Total: st.Blocks * bs, Free: st.Bavail * bs}
}

// Alerts reads the active alerts from the monitor's JSON state file
// ({"active": {key: text}}). A missing file or empty path returns (nil, nil).
func Alerts(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st struct {
		Active map[string]string `json:"active"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("invalid state file: %w", err)
	}
	return st.Active, nil
}

// SortedKeys returns the map keys in order, for stable output.
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
