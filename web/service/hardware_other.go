//go:build !linux

package service

func readOSPrettyName() string {
	return ""
}

func readHardwareBoard() HardwareBoard {
	return HardwareBoard{}
}

func readHardwareDisks() []HardwareDisk {
	return []HardwareDisk{}
}

func readInterfaceSpeed(name string) int {
	return 0
}
