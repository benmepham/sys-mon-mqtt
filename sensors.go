package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Sensor describes one Home Assistant sensor and how to read its value.
type Sensor struct {
	Key         string
	Name        string
	DeviceClass string
	StateClass  string
	Unit        string
	Icon        string
	Read        func() (any, error)
}

// buildSensors returns the sensors enabled in settings, in a fixed order.
func buildSensors(cfg Config) []Sensor {
	all := []Sensor{
		{Key: "temperature", Name: "Temperature", DeviceClass: "temperature", StateClass: "measurement", Unit: "°C", Icon: "thermometer", Read: readTemperature},
		{Key: "clock_speed", Name: "Clock Speed", StateClass: "measurement", Unit: "MHz", Read: readClockSpeed},
		{Key: "disk_use", Name: "Disk Use", StateClass: "measurement", Unit: "%", Icon: "micro-sd", Read: func() (any, error) { return readDiskUsage("/") }},
		{Key: "memory_use", Name: "Memory Use", StateClass: "measurement", Unit: "%", Icon: "memory", Read: readMemoryUsage},
		{Key: "cpu_usage", Name: "CPU Usage", StateClass: "measurement", Unit: "%", Icon: "chip", Read: readCPUUsage},
		{Key: "load_1m", Name: "Load 1m", StateClass: "measurement", Unit: "%", Icon: "cpu-64-bit", Read: func() (any, error) { return readLoad(0) }},
		{Key: "load_5m", Name: "Load 5m", StateClass: "measurement", Unit: "%", Icon: "cpu-64-bit", Read: func() (any, error) { return readLoad(1) }},
		{Key: "load_15m", Name: "Load 15m", StateClass: "measurement", Unit: "%", Icon: "cpu-64-bit", Read: func() (any, error) { return readLoad(2) }},
		{Key: "swap_usage", Name: "Swap Usage", StateClass: "measurement", Unit: "%", Icon: "harddisk", Read: readSwapUsage},
		{Key: "last_boot", Name: "Last Boot", DeviceClass: "timestamp", Icon: "clock", Read: readLastBoot},
		{Key: "hostname", Name: "Hostname", Icon: "card-account-details", Read: func() (any, error) { return os.Hostname() }},
		{Key: "host_ip", Name: "Host IP", Icon: "lan", Read: readHostIP},
		{Key: "host_os", Name: "Host OS", Icon: "linux", Read: readHostOS},
		{Key: "host_arch", Name: "Host Architecture", Icon: "chip", Read: func() (any, error) { return host.KernelArch() }},
		{Key: "last_message", Name: "Last Message", DeviceClass: "timestamp", Icon: "clock-check", Read: func() (any, error) { return time.Now().UTC().Format(time.RFC3339), nil }},
		{Key: "updates", Name: "Updates", Icon: "cellphone-arrow-down", Read: readUpdates},
	}

	var sensors []Sensor
	for _, s := range all {
		if !sensorEnabled(cfg.Sensors, s.Key) {
			continue
		}
		sensors = append(sensors, s)
	}
	return append(sensors, buildDriveSensors(cfg.Sensors)...)
}

// sensorEnabled matches the Python rules: missing means on, false or null means off.
func sensorEnabled(settings map[string]any, key string) bool {
	v, ok := settings[key]
	if !ok {
		return true
	}
	if v == nil || v == false {
		return false
	}
	return true
}

// buildDriveSensors adds a sensor per entry in sensors.external_drives.
// Like the Python version, drives must be mounted at startup.
func buildDriveSensors(settings map[string]any) []Sensor {
	drives, _ := settings["external_drives"].(map[string]any)
	var sensors []Sensor
	for name, value := range drives {
		mountPoint, _ := value.(string)
		if isZFS(mountPoint) {
			if _, err := readZpoolUsage(name); err != nil {
				log.Printf("zpool %s not readable, skipping: %v", name, err)
				continue
			}
			sensors = append(sensors, Sensor{
				Key: "zpool_use_" + strings.ToLower(name), Name: "Zpool Use " + name,
				StateClass: "measurement", Unit: "%", Icon: "harddisk",
				Read: func() (any, error) { return readZpoolUsage(name) },
			})
			continue
		}
		if _, err := readDiskUsage(mountPoint); err != nil {
			log.Printf("drive %s (%s) not mounted, skipping: %v", name, mountPoint, err)
			continue
		}
		sensors = append(sensors, Sensor{
			Key: "disk_use_" + strings.ToLower(name), Name: "Disk Use " + name,
			StateClass: "measurement", Unit: "%", Icon: "harddisk",
			Read: func() (any, error) { return readDiskUsage(mountPoint) },
		})
	}
	return sensors
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// readTemperature uses the same sensor names as the Python version.
func readTemperature() (any, error) {
	names := []string{"cpu-thermal", "cpu_thermal", "coretemp", "soc_thermal", "k10temp"}
	for _, name := range names {
		hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
		for _, dir := range hwmons {
			if readTrimmed(filepath.Join(dir, "name")) == name {
				return readMilliDegrees(filepath.Join(dir, "temp1_input"))
			}
		}
	}
	return nil, errors.New("no known temperature sensor found")
}

func readMilliDegrees(path string) (any, error) {
	v, err := strconv.ParseFloat(readTrimmed(path), 64)
	if err != nil {
		return nil, err
	}
	return round(v/1000, 1), nil
}

// readTrimmed returns the file contents without surrounding whitespace, or "" on error.
func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readClockSpeed returns the average current CPU frequency in MHz.
func readClockSpeed() (any, error) {
	files, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	var total float64
	var count int
	for _, f := range files {
		khz, err := strconv.ParseFloat(readTrimmed(f), 64)
		if err == nil {
			total += khz / 1000
			count++
		}
	}
	if count == 0 {
		return nil, errors.New("no CPU frequency found")
	}
	return int(total / float64(count)), nil
}

func readDiskUsage(path string) (any, error) {
	usage, err := disk.Usage(path)
	if err != nil {
		return nil, err
	}
	return round(usage.UsedPercent, 1), nil
}

// readMemoryUsage matches psutil: reclaimable cache and buffers don't count as used.
func readMemoryUsage() (any, error) {
	m, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	return round(float64(m.Total-m.Available)/float64(m.Total)*100, 1), nil
}

func readSwapUsage() (any, error) {
	s, err := mem.SwapMemory()
	if err != nil {
		return nil, err
	}
	return round(s.UsedPercent, 1), nil
}

// readCPUUsage returns usage since the previous call (primed once at startup).
func readCPUUsage() (any, error) {
	percents, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	if len(percents) == 0 {
		return nil, errors.New("no CPU usage data")
	}
	return round(percents[0], 1), nil
}

// readLoad returns the load average as a percentage of CPU count.
func readLoad(index int) (any, error) {
	avg, err := load.Avg()
	if err != nil {
		return nil, err
	}
	count, err := cpu.Counts(true)
	if err != nil {
		return nil, err
	}
	values := []float64{avg.Load1, avg.Load5, avg.Load15}
	return round(values[index]/float64(count)*100, 1), nil
}

func readLastBoot() (any, error) {
	boot, err := host.BootTime()
	if err != nil {
		return nil, err
	}
	return time.Unix(int64(boot), 0).UTC().Format(time.RFC3339), nil
}

// readHostIP finds the outbound interface address. UDP dial sends no packets.
func readHostIP() (any, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func readHostOS() (any, error) {
	if name := osRelease()["PRETTY_NAME"]; name != "" {
		return name, nil
	}
	return nil, errors.New("PRETTY_NAME not in /etc/os-release")
}

// osRelease parses /etc/os-release into a map.
func osRelease() map[string]string {
	values := map[string]string{}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, `"`)
		}
	}
	return values
}

// Cached apt result, as apt-get is slow on a Pi.
var (
	updatesCount     int
	updatesErr       error
	updatesCheckedAt time.Time
)

func readUpdates() (any, error) {
	if time.Since(updatesCheckedAt) > 15*time.Minute {
		updatesCount, updatesErr = countUpdates()
		updatesCheckedAt = time.Now()
	}
	if updatesErr != nil {
		return nil, updatesErr
	}
	return updatesCount, nil
}

// countUpdates counts pending upgrades from a simulated apt-get upgrade.
func countUpdates() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "apt-get", "-s", "upgrade").Output()
	if err != nil {
		return 0, fmt.Errorf("apt-get: %w", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Inst ") {
			count++
		}
	}
	return count, nil
}

func isZFS(mountPoint string) bool {
	partitions, err := disk.Partitions(true)
	if err != nil {
		return false
	}
	for _, p := range partitions {
		if p.Mountpoint == mountPoint && p.Fstype == "zfs" {
			return true
		}
	}
	return false
}

func readZpoolUsage(pool string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-p", "-o", "capacity", pool).Output()
	if err != nil {
		return nil, fmt.Errorf("zpool list %s: %w", pool, err)
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
