package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func getWiFiInterfaces() map[string]bool {
	wifi := make(map[string]bool)
	out, err := exec.Command("networksetup", "-listallhardwareports").Output()
	if err != nil {
		return wifi
	}
	lines := strings.Split(string(out), "\n")
	re := regexp.MustCompile(`Device:\s*(en\d+)`)
	for i, line := range lines {
		if strings.Contains(line, "Wi-Fi") || strings.Contains(line, "AirPort") {
			end := i + 5
			if end > len(lines) {
				end = len(lines)
			}
			for _, l := range lines[i:end] {
				if m := re.FindStringSubmatch(l); m != nil {
					wifi[m[1]] = true
				}
			}
		}
	}
	return wifi
}

func getInterfaceStatus(iface string) (status, ip string) {
	out, err := exec.Command("ifconfig", iface).Output()
	if err != nil {
		return "inactive", ""
	}
	s := string(out)
	if strings.Contains(s, "status: active") {
		status = "active"
	} else {
		status = "inactive"
	}
	re := regexp.MustCompile(`inet (\d+\.\d+\.\d+\.\d+)`)
	if m := re.FindStringSubmatch(s); m != nil {
		ip = m[1]
	}
	return
}

func listEthernetInterfaces(wifi map[string]bool) []string {
	out, err := exec.Command("ifconfig", "-l").Output()
	if err != nil {
		return nil
	}
	var result []string
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, "lo") || wifi[name] {
			continue
		}
		if strings.HasPrefix(name, "en") || strings.HasPrefix(name, "eth") {
			result = append(result, name)
		}
	}
	return result
}

func getUsedSubnets() map[string]bool {
	used := make(map[string]bool)
	out, err := exec.Command("ifconfig").Output()
	if err != nil {
		return used
	}
	re := regexp.MustCompile(`inet (\d+)\.(\d+)\.(\d+)\.\d+`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		if m[1] != "127" {
			used[fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])] = true
		}
	}
	return used
}

func findFreeSubnet(preferred string) string {
	return preferred
}

func configureInterface(iface, ip, netmask string) error {
	return exec.Command("ifconfig", iface, ip, "netmask", netmask).Run()
}

func releaseInterface(iface string) {
	exec.Command("ipconfig", "set", iface, "DHCP").Run()
}

func waitForNewCable(wifi map[string]bool) string {
	seen := make(map[string]string)
	for {
		for _, iface := range listEthernetInterfaces(wifi) {
			status, _ := getInterfaceStatus(iface)
			if status == "active" && seen[iface] != "active" {
				seen[iface] = status
				return iface
			}
			seen[iface] = status
		}
		time.Sleep(time.Second)
	}
}

func waitForLinkLoss(ctx context.Context, iface string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
			status, _ := getInterfaceStatus(iface)
			if status != "active" {
				return
			}
		}
	}
}
