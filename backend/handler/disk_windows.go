//go:build windows

package handler

import (
	"syscall"
	"unsafe"
)

func init() {
	getDiskSpace = getDiskSpaceWindows
	getAllDisks = getAllDisksWindows
}

func getDiskSpaceWindows(path string) (*diskSpaceInfo, error) {
	rootPath := getDriveRoot(path)

	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64

	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.MustFindProc("GetDiskFreeSpaceExW")

	rootPathPtr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		return nil, err
	}

	ret, _, err := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(rootPathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)

	if ret == 0 {
		return nil, err
	}

	return &diskSpaceInfo{
		total: totalNumberOfBytes,
		free:  freeBytesAvailable,
	}, nil
}

func getAllDisksWindows() ([]*DiskInfo, error) {
	var disks []*DiskInfo
	
	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	getLogicalDrives := kernel32.MustFindProc("GetLogicalDrives")
	
	ret, _, _ := getLogicalDrives.Call()
	drives := uint32(ret)
	
	for i := 0; i < 26; i++ {
		if drives&(1<<uint(i)) != 0 {
			driveLetter := string('A' + i)
			drivePath := driveLetter + ":/"
			
			var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
			
			getDiskFreeSpaceEx := kernel32.MustFindProc("GetDiskFreeSpaceExW")
			drivePtr, _ := syscall.UTF16PtrFromString(drivePath)
			
			ret, _, _ := getDiskFreeSpaceEx.Call(
				uintptr(unsafe.Pointer(drivePtr)),
				uintptr(unsafe.Pointer(&freeBytesAvailable)),
				uintptr(unsafe.Pointer(&totalNumberOfBytes)),
				uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
			)
			
			if ret == 0 || totalNumberOfBytes == 0 {
				continue
			}
			
			used := totalNumberOfBytes - freeBytesAvailable
			usedPercent := float64(used) / float64(totalNumberOfBytes) * 100
			
			disks = append(disks, &DiskInfo{
				MountPoint:  drivePath,
				Device:      driveLetter + ":",
				FsType:      getDriveType(driveLetter),
				Total:       totalNumberOfBytes,
				Used:        used,
				Free:        freeBytesAvailable,
				UsedPercent: usedPercent,
				FreePercent: 100 - usedPercent,
				ColorClass:  getColorClass(usedPercent),
			})
		}
	}
	
	return disks, nil
}

func getDriveRoot(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		return path[:2] + "\\"
	}

	return "."
}

func getDriveType(driveLetter string) string {
	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	getDriveType := kernel32.MustFindProc("GetDriveTypeW")
	
	drivePath := driveLetter + ":/"
	drivePtr, _ := syscall.UTF16PtrFromString(drivePath)
	
	ret, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(drivePtr)))
	
	switch ret {
	case 2:
		return "Removable"
	case 3:
		return "Local Disk"
	case 4:
		return "Network"
	case 5:
		return "CD-ROM"
	case 6:
		return "RAM Disk"
	default:
		return "Unknown"
	}
}