//go:build linux

package service

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func readOSPrettyName() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				value := strings.TrimSpace(strings.TrimPrefix(line, "PRETTY_NAME="))
				value = strings.Trim(value, `"'`)
				if value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func readHardwareBoard() HardwareBoard {
	return HardwareBoard{
		Vendor:      readSysString("/sys/class/dmi/id/sys_vendor"),
		Product:     readSysString("/sys/class/dmi/id/product_name"),
		BoardVendor: readSysString("/sys/class/dmi/id/board_vendor"),
		BoardName:   readSysString("/sys/class/dmi/id/board_name"),
		BiosVendor:  readSysString("/sys/class/dmi/id/bios_vendor"),
		BiosVersion: readSysString("/sys/class/dmi/id/bios_version"),
		BiosDate:    readSysString("/sys/class/dmi/id/bios_date"),
	}
}

func readHardwareDisks() []HardwareDisk {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return []HardwareDisk{}
	}
	disks := make([]HardwareDisk, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "sr") ||
			strings.HasPrefix(name, "fd") {
			continue
		}
		base := filepath.Join("/sys/block", name)
		sectors := readSysInt64(filepath.Join(base, "size"))
		if sectors <= 0 {
			continue
		}
		model := readSysString(filepath.Join(base, "device/model"))
		if model == "" || strings.HasPrefix(model, "0x") {
			model = ""
		}
		rotational := readSysString(filepath.Join(base, "queue/rotational")) != "0"
		virtual := strings.HasPrefix(name, "vd") ||
			strings.HasPrefix(name, "xvd") ||
			strings.HasPrefix(name, "fd") ||
			strings.HasPrefix(name, "dm-") ||
			strings.HasPrefix(name, "md")
		disks = append(disks, HardwareDisk{
			Name:       name,
			Model:      model,
			Size:       uint64(sectors) * 512,
			Rotational: rotational,
			Virtual:    virtual,
			Removable:  readSysString(filepath.Join(base, "removable")) == "1",
		})
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks
}

func readInterfaceSpeed(name string) int {
	value := readSysString(filepath.Join("/sys/class/net", name, "speed"))
	speed, err := strconv.Atoi(value)
	if err != nil || speed <= 0 {
		return 0
	}
	return speed
}

func readSysString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readSysInt64(path string) int64 {
	value, err := strconv.ParseInt(readSysString(path), 10, 64)
	if err != nil {
		return 0
	}
	return value
}
