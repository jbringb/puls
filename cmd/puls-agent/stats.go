package main

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/jbringb/puls/internal/model"
	"github.com/jbringb/puls/internal/ws"
)

func diskRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

// heartbeatData samples current host stats for a single heartbeat. It
// blocks briefly (~300ms) to get a non-zero CPU sample from gopsutil.
func heartbeatData(ctx context.Context) ws.HeartbeatData {
	data := ws.HeartbeatData{}

	if pct, err := cpu.PercentWithContext(ctx, 300*time.Millisecond, false); err == nil && len(pct) > 0 {
		data.CPUPercent = float32(pct[0])
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		data.MemoryPercent = float32(vm.UsedPercent)
	}
	if du, err := disk.UsageWithContext(ctx, diskRoot()); err == nil {
		data.DiskPercent = float32(du.UsedPercent)
	}
	if info, err := host.InfoWithContext(ctx); err == nil {
		data.UptimeSeconds = int64(info.Uptime)
		data.OSVersion = fmt.Sprintf("%s %s", info.Platform, info.PlatformVersion)
	}

	return data
}

// diagnosticPayload gathers a richer, scope-dependent snapshot in response
// to an admin-initiated diag_request.
func diagnosticPayload(ctx context.Context, scope model.DiagnosticScope) map[string]any {
	payload := map[string]any{}

	hostInfo, _ := host.InfoWithContext(ctx)
	if hostInfo != nil {
		payload["hostname"] = hostInfo.Hostname
		payload["platform"] = fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)
		payload["kernelVersion"] = hostInfo.KernelVersion
		payload["uptimeSeconds"] = hostInfo.Uptime
	}
	if cores, err := cpu.CountsWithContext(ctx, true); err == nil {
		payload["cpuCores"] = cores
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		payload["memoryTotalBytes"] = vm.Total
		payload["memoryUsedPercent"] = vm.UsedPercent
	}

	switch scope {
	case model.ScopeNetwork:
		payload["interfaces"] = networkInterfaces(ctx)
	case model.ScopeProcesses:
		payload["processes"] = topProcesses(ctx, 10)
	case model.ScopeStorage:
		payload["partitions"] = storagePartitions(ctx)
	default: // full
		payload["interfaces"] = networkInterfaces(ctx)
		payload["processes"] = topProcesses(ctx, 5)
		payload["partitions"] = storagePartitions(ctx)
	}

	return payload
}

func networkInterfaces(ctx context.Context) []map[string]any {
	ifaces, err := psnet.InterfacesWithContext(ctx)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs := make([]string, 0, len(iface.Addrs))
		for _, a := range iface.Addrs {
			addrs = append(addrs, a.Addr)
		}
		out = append(out, map[string]any{
			"name":  iface.Name,
			"addrs": addrs,
		})
	}
	return out
}

func topProcesses(ctx context.Context, limit int) []map[string]any {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil
	}

	type procStat struct {
		pid    int32
		name   string
		cpuPct float64
		memPct float32
	}
	stats := make([]procStat, 0, len(procs))
	for _, p := range procs {
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue
		}
		cpuPct, _ := p.CPUPercentWithContext(ctx)
		memPct, _ := p.MemoryPercentWithContext(ctx)
		stats = append(stats, procStat{pid: p.Pid, name: name, cpuPct: cpuPct, memPct: memPct})
	}

	sort.Slice(stats, func(i, j int) bool { return stats[i].cpuPct > stats[j].cpuPct })
	if len(stats) > limit {
		stats = stats[:limit]
	}

	out := make([]map[string]any, 0, len(stats))
	for _, s := range stats {
		out = append(out, map[string]any{
			"pid":        s.pid,
			"name":       s.name,
			"cpuPercent": s.cpuPct,
			"memPercent": s.memPct,
		})
	}
	return out
}

func storagePartitions(ctx context.Context) []map[string]any {
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(partitions))
	for _, p := range partitions {
		if p.Mountpoint == "" {
			continue
		}
		entry := map[string]any{
			"device":     p.Device,
			"mountpoint": p.Mountpoint,
			"fstype":     p.Fstype,
		}
		if usage, err := disk.UsageWithContext(ctx, p.Mountpoint); err == nil {
			entry["usedPercent"] = usage.UsedPercent
			entry["totalBytes"] = usage.Total
		}
		out = append(out, entry)
	}
	return out
}
