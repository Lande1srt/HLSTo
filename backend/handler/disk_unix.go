//go:build linux || darwin || freebsd || openbsd

package handler

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func init() {
	getDiskSpace = getDiskSpaceUnix
	getAllDisks = getAllDisksUnix
}

func getDiskSpaceUnix(path string) (*diskSpaceInfo, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return nil, err
	}

	return &diskSpaceInfo{
		total: stat.Blocks * uint64(stat.Bsize),
		free:  stat.Bfree * uint64(stat.Bsize),
	}, nil
}

func getAllDisksUnix() ([]*DiskInfo, error) {
	var disks []*DiskInfo
	
	// 首先尝试从 /proc/mounts 获取磁盘信息
	file, err := os.Open("/proc/mounts")
	if err == nil {
		defer file.Close()
		
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) < 4 {
				continue
			}
			
			device := parts[0]
			mountPoint := parts[1]
			fsType := parts[2]
			
			if !isValidMount(device, fsType) {
				continue
			}
			
			var stat syscall.Statfs_t
			err := syscall.Statfs(mountPoint, &stat)
			if err != nil {
				continue
			}
			
			total := stat.Blocks * uint64(stat.Bsize)
			if total == 0 {
				continue
			}
			
			free := stat.Bfree * uint64(stat.Bsize)
			used := total - free
			usedPercent := float64(used) / float64(total) * 100
			
			disks = append(disks, &DiskInfo{
				MountPoint:  mountPoint,
				Device:      device,
				FsType:      fsType,
				Total:       total,
				Used:        used,
				Free:        free,
				UsedPercent: usedPercent,
				FreePercent: 100 - usedPercent,
				ColorClass:  getColorClass(usedPercent),
			})
		}
	}
	
	if len(disks) == 0 {
		disks = getDisksFromCommonMounts()
	}
	
	disks = deduplicateDisks(disks)
	
	return disks, nil
}

func deduplicateDisks(disks []*DiskInfo) []*DiskInfo {
	seen := make(map[string]*DiskInfo)
	
	for _, disk := range disks {
		key := fmt.Sprintf("%d_%d", disk.Total, disk.Free)
		
		if existing, ok := seen[key]; ok {
			// 如果已存在，比较路径长度，保留路径更短的
			if len(disk.MountPoint) < len(existing.MountPoint) {
				seen[key] = disk
			}
		} else {
			seen[key] = disk
		}
	}
	
	result := make([]*DiskInfo, 0, len(seen))
	for _, disk := range seen {
		result = append(result, disk)
	}
	
	return result
}

func getDisksFromCommonMounts() []*DiskInfo {
	var disks []*DiskInfo
	
	commonMounts := []string{"/", "/home", "/boot", "/var", "/opt", "/tmp", "/Users", "/Volumes"}
	seen := make(map[string]bool)
	
	for _, mountPoint := range commonMounts {
		if seen[mountPoint] {
			continue
		}
		
		var stat syscall.Statfs_t
		err := syscall.Statfs(mountPoint, &stat)
		if err != nil {
			continue
		}
		
		total := stat.Blocks * uint64(stat.Bsize)
		if total == 0 {
			continue
		}
		
		free := stat.Bfree * uint64(stat.Bsize)
		used := total - free
		usedPercent := float64(used) / float64(total) * 100
		
		disks = append(disks, &DiskInfo{
			MountPoint:  mountPoint,
			Device:      mountPoint,
			FsType:      "unknown",
			Total:       total,
			Used:        used,
			Free:        free,
			UsedPercent: usedPercent,
			FreePercent: 100 - usedPercent,
			ColorClass:  getColorClass(usedPercent),
		})
		seen[mountPoint] = true
	}
	
	return disks
}



func isValidMount(device, fsType string) bool {
	ignoreTypes := []string{"tmpfs", "devtmpfs", "proc", "sysfs", "devpts", "tmpfs", "cgroup", "cgroup2", "securityfs", "pstore", "efivarfs", "hugetlbfs", "mqueue", "debugfs", "tracefs", "fusectl"}
	
	for _, t := range ignoreTypes {
		if fsType == t {
			return false
		}
	}
	
	if strings.HasPrefix(device, "/dev/loop") {
		return false
	}
	
	if strings.HasPrefix(device, "/dev/sr") {
		return false
	}
	
	return true
}