package service

import (
	"runtime"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

type HardwareHost struct {
	Hostname       string `json:"hostname"`
	OS             string `json:"os"`
	Kernel         string `json:"kernel"`
	Arch           string `json:"arch"`
	Virtualization string `json:"virtualization"`
	Uptime         uint64 `json:"uptime"`
	BootTime       uint64 `json:"bootTime"`
	Processes      uint64 `json:"processes"`
}

type HardwareBoard struct {
	Vendor      string `json:"vendor"`
	Product     string `json:"product"`
	BoardVendor string `json:"boardVendor"`
	BoardName   string `json:"boardName"`
	BiosVendor  string `json:"biosVendor"`
	BiosVersion string `json:"biosVersion"`
	BiosDate    string `json:"biosDate"`
}

type HardwareCPU struct {
	Model         string  `json:"model"`
	Vendor        string  `json:"vendor"`
	PhysicalCores int     `json:"physicalCores"`
	LogicalCores  int     `json:"logicalCores"`
	Mhz           float64 `json:"mhz"`
	CacheKB       int32   `json:"cacheKB"`
	Microcode     string  `json:"microcode"`
}

type HardwareMemory struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
	Cached    uint64 `json:"cached"`
	Buffers   uint64 `json:"buffers"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapUsed  uint64 `json:"swapUsed"`
}

type HardwareDisk struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Size       uint64 `json:"size"`
	Rotational bool   `json:"rotational"`
	Virtual    bool   `json:"virtual"`
	Removable  bool   `json:"removable"`
}

type HardwarePartition struct {
	Device      string  `json:"device"`
	Mountpoint  string  `json:"mountpoint"`
	Fstype      string  `json:"fstype"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"usedPercent"`
}

type HardwareNetwork struct {
	Name      string   `json:"name"`
	Mac       string   `json:"mac"`
	MTU       int      `json:"mtu"`
	Up        bool     `json:"up"`
	SpeedMbps int      `json:"speedMbps"`
	IPv4      []string `json:"ipv4"`
	IPv6      []string `json:"ipv6"`
}

type HardwareInfo struct {
	Host       HardwareHost        `json:"host"`
	Board      HardwareBoard       `json:"board"`
	CPU        HardwareCPU         `json:"cpu"`
	Memory     HardwareMemory      `json:"memory"`
	Disks      []HardwareDisk      `json:"disks"`
	Partitions []HardwarePartition `json:"partitions"`
	Networks   []HardwareNetwork   `json:"networks"`
	Load       []float64           `json:"load"`
	Temps      []TempSensor        `json:"temps"`
}

func (s *ServerService) GetHardwareInfo() (*HardwareInfo, error) {
	info := &HardwareInfo{
		Disks:      []HardwareDisk{},
		Partitions: []HardwarePartition{},
		Networks:   []HardwareNetwork{},
		Load:       []float64{},
		Temps:      []TempSensor{},
	}

	if hostInfo, err := host.Info(); err == nil {
		info.Host.Hostname = hostInfo.Hostname
		info.Host.OS = readOSPrettyName()
		if info.Host.OS == "" {
			info.Host.OS = strings.TrimSpace(strings.Join([]string{hostInfo.Platform, hostInfo.PlatformVersion}, " "))
		}
		info.Host.Kernel = hostInfo.KernelVersion
		info.Host.Arch = hostInfo.KernelArch
		if info.Host.Arch == "" {
			info.Host.Arch = runtime.GOARCH
		}
		if hostInfo.VirtualizationSystem != "" {
			info.Host.Virtualization = hostInfo.VirtualizationSystem
			if hostInfo.VirtualizationRole != "" {
				info.Host.Virtualization += " (" + hostInfo.VirtualizationRole + ")"
			}
		}
		info.Host.Uptime = hostInfo.Uptime
		info.Host.BootTime = hostInfo.BootTime
		info.Host.Processes = hostInfo.Procs
	}
	info.Board = readHardwareBoard()

	if physical, err := cpu.Counts(true); err == nil {
		info.CPU.PhysicalCores = physical
	}
	if logical, err := cpu.Counts(false); err == nil {
		info.CPU.LogicalCores = logical
	}
	if cpuInfos, err := cpu.Info(); err == nil && len(cpuInfos) > 0 {
		first := cpuInfos[0]
		info.CPU.Model = strings.TrimSpace(first.ModelName)
		info.CPU.Vendor = strings.TrimSpace(first.VendorID)
		info.CPU.Microcode = strings.TrimSpace(first.Microcode)
		info.CPU.CacheKB = first.CacheSize
		for _, ci := range cpuInfos {
			if ci.Mhz > info.CPU.Mhz {
				info.CPU.Mhz = ci.Mhz
			}
			if info.CPU.CacheKB == 0 && ci.CacheSize > 0 {
				info.CPU.CacheKB = ci.CacheSize
			}
			if info.CPU.Microcode == "" {
				info.CPU.Microcode = strings.TrimSpace(ci.Microcode)
			}
		}
		if info.CPU.LogicalCores == 0 {
			info.CPU.LogicalCores = len(cpuInfos)
		}
	}

	if memory, err := mem.VirtualMemory(); err == nil {
		info.Memory.Total = memory.Total
		info.Memory.Used = memory.Used
		info.Memory.Available = memory.Available
		info.Memory.Cached = memory.Cached
		info.Memory.Buffers = memory.Buffers
	}
	if swap, err := mem.SwapMemory(); err == nil {
		info.Memory.SwapTotal = swap.Total
		info.Memory.SwapUsed = swap.Used
	}

	info.Disks = readHardwareDisks()
	info.Partitions = readHardwarePartitions()
	info.Networks = readHardwareNetworks()

	if averages, err := load.Avg(); err == nil {
		info.Load = []float64{averages.Load1, averages.Load5, averages.Load15}
	}

	if temps, err := sensors.SensorsTemperatures(); err == nil {
		for _, t := range temps {
			if t.Temperature <= 0 || t.Temperature >= 150 {
				continue
			}
			name := strings.TrimSpace(t.SensorKey)
			if name == "" {
				name = "sensor"
			}
			info.Temps = append(info.Temps, TempSensor{Name: name, Temp: t.Temperature})
			if len(info.Temps) >= 8 {
				break
			}
		}
	}

	return info, nil
}

func readHardwarePartitions() []HardwarePartition {
	parts, err := disk.Partitions(false)
	if err != nil {
		return []HardwarePartition{}
	}
	result := make([]HardwarePartition, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if skipHardwareFilesystem(part.Fstype) || part.Mountpoint == "" {
			continue
		}
		if strings.HasPrefix(part.Mountpoint, "/var/lib/docker") ||
			strings.HasPrefix(part.Mountpoint, "/var/lib/kubelet") ||
			strings.HasPrefix(part.Mountpoint, "/var/lib/containers") ||
			strings.HasPrefix(part.Mountpoint, "/snap") {
			continue
		}
		key := part.Device + "|" + part.Mountpoint
		if seen[key] {
			continue
		}
		usage, err := disk.Usage(part.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}
		seen[key] = true
		result = append(result, HardwarePartition{
			Device:      part.Device,
			Mountpoint:  part.Mountpoint,
			Fstype:      part.Fstype,
			Total:       usage.Total,
			Used:        usage.Used,
			Free:        usage.Free,
			UsedPercent: usage.UsedPercent,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Mountpoint == "/" {
			return true
		}
		if result[j].Mountpoint == "/" {
			return false
		}
		return result[i].Mountpoint < result[j].Mountpoint
	})
	if len(result) > 16 {
		result = result[:16]
	}
	return result
}

func readHardwareNetworks() []HardwareNetwork {
	ifaces, err := gnet.Interfaces()
	if err != nil {
		return []HardwareNetwork{}
	}
	result := make([]HardwareNetwork, 0, len(ifaces))
	for _, iface := range ifaces {
		if skipHardwareInterface(iface.Name) {
			continue
		}
		network := HardwareNetwork{
			Name:      iface.Name,
			Mac:       iface.HardwareAddr,
			MTU:       iface.MTU,
			Up:        hasFlag(iface.Flags, "up"),
			SpeedMbps: readInterfaceSpeed(iface.Name),
			IPv4:      []string{},
			IPv6:      []string{},
		}
		for _, addr := range iface.Addrs {
			ip := strings.TrimSpace(strings.Split(addr.Addr, "/")[0])
			switch {
			case strings.Contains(ip, ".") && !strings.HasPrefix(ip, "127."):
				network.IPv4 = append(network.IPv4, ip)
			case strings.Contains(ip, ":") && !strings.HasPrefix(ip, "fe80:") && ip != "::1":
				network.IPv6 = append(network.IPv6, ip)
			}
		}
		result = append(result, network)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func skipHardwareFilesystem(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup", "cgroup2",
		"pstore", "securityfs", "debugfs", "tracefs", "configfs", "fusectl",
		"mqueue", "hugetlbfs", "binfmt_misc", "autofs", "nsfs", "squashfs",
		"iso9660", "ramfs", "rpc_pipefs", "nfs", "nfs4", "cifs", "smb3",
		"fuse.gvfsd-fuse", "fuse.portal":
		return true
	}
	return false
}

func skipHardwareInterface(name string) bool {
	if name == "lo" {
		return true
	}
	prefixes := []string{
		"docker", "veth", "br-", "virbr", "cni", "flannel", "cali",
		"vxlan", "kube", "tailscale", "wg", "tun", "tap", "dummy", "ifb",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func hasFlag(flags []string, flag string) bool {
	for _, item := range flags {
		if item == flag {
			return true
		}
	}
	return false
}
