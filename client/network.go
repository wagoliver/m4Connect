package main

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// hidden returns an exec.Cmd that won't show a console window.
func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// ── Ethernet Detection (Windows) ──────────────────────────────────────────────

type EthernetIface struct {
	Name   string
	Status string // "active" | "inactive"
}

func getEthernetInterfaces() []EthernetIface {
	ps := `Get-NetAdapter | Where-Object {` +
		`$_.MediaType -ne 'Native 802.11' -and ` +
		`$_.Name -notmatch 'Wi-Fi|Wireless|Loopback|VPN|Virtual|Bluetooth|vEthernet'` +
		`} | ForEach-Object { $_.Name + '|' + $_.Status }`
	out, err := hidden("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return nil
	}
	var result []EthernetIface
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		status := strings.TrimSpace(strings.ToLower(parts[1]))
		active := "inactive"
		if status == "up" {
			active = "active"
		}
		result = append(result, EthernetIface{Name: name, Status: active})
	}
	return result
}

// knownIfaceIfActive returns name if the interface is currently active, else "".
func knownIfaceIfActive(name string) string {
	if name == "" {
		return ""
	}
	for _, iface := range getEthernetInterfaces() {
		if iface.Name == name && iface.Status == "active" {
			return name
		}
	}
	return ""
}

func waitForEthernetCable() string {
	seen := make(map[string]string)
	for _, iface := range getEthernetInterfaces() {
		seen[iface.Name] = iface.Status
	}

	for {
		time.Sleep(time.Second)
		for _, iface := range getEthernetInterfaces() {
			prev := seen[iface.Name]
			seen[iface.Name] = iface.Status
			if iface.Status == "active" && prev != "active" {
				return iface.Name
			}
		}
	}
}

// ── IP Configuration (Windows — requires admin) ───────────────────────────────

func configureIPWindows(ifaceName, ip, netmask string) error {
	_, err := configureIPWindowsDebug(ifaceName, ip, netmask)
	return err
}

func configureIPWindowsDebug(ifaceName, ip, netmask string) (string, error) {
	args := []string{
		"interface", "ip", "set", "address",
		fmt.Sprintf("name=%s", ifaceName),
		"static", ip, netmask,
	}
	out, err := hidden("netsh", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("netsh: %v — %s", err, string(out))
	}
	return string(out), nil
}


func getInterfaceIP(ifaceName string) string {
	ps := fmt.Sprintf(`(Get-NetIPAddress -InterfaceAlias '%s' -AddressFamily IPv4 -ErrorAction SilentlyContinue).IPAddress`, ifaceName)
	out, err := hidden("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func releaseIPWindows(ifaceName string) {
	hidden("netsh", "interface", "ip", "set", "address",
		fmt.Sprintf("name=%s", ifaceName), "dhcp").Run()
}

// ── Handshake ─────────────────────────────────────────────────────────────────

type HandshakeResult struct {
	MacIP      string
	ClientIP   string
	PortalPort int
	Hostname   string
}

func sendHandshake(serverIP, clientIP, token string, port int, timeout time.Duration) (*HandshakeResult, error) {
	localAddr, err := net.ResolveUDPAddr("udp", clientIP+":0")
	if err != nil {
		return nil, fmt.Errorf("endereço local inválido: %v", err)
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", serverIP, port))
	if err != nil {
		return nil, fmt.Errorf("endereço remoto inválido: %v", err)
	}

	// Retry bind until IP leaves DAD/tentative state (up to 5s)
	var conn *net.UDPConn
	for i := 0; i < 10; i++ {
		conn, err = net.DialUDP("udp", localAddr, remoteAddr)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))
	msg := "M4HELLO:" + token
	if _, err := conn.Write([]byte(msg)); err != nil {
		return nil, err
	}

	// Retry sending every 2s in case Mac is still restarting
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.Write([]byte(msg))
			case <-done:
				return
			}
		}
	}()

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("sem resposta do Mac Mini")
	}

	resp := string(buf[:n])
	if !strings.HasPrefix(resp, "M4ACK:") {
		return nil, fmt.Errorf("resposta inesperada: %s", resp)
	}

	parts := strings.Split(resp[len("M4ACK:"):], ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("formato de ACK inválido")
	}

	var portalPort int
	fmt.Sscanf(parts[2], "%d", &portalPort)
	hostname := "Mac Mini M4"
	if len(parts) >= 4 && parts[3] != "" {
		hostname = parts[3]
	}
	return &HandshakeResult{MacIP: parts[0], ClientIP: parts[1], PortalPort: portalPort, Hostname: hostname}, nil
}

func pingRTT(host string) (float64, error) {
	out, err := hidden("ping", "-n", "1", "-w", "2000", host).Output()
	if err != nil {
		return 0, err
	}
	re := regexp.MustCompile(`[Tt]ime[<=](\d+)ms|[Mm]édia\s*=\s*(\d+)ms|[Aa]verage\s*=\s*(\d+)ms`)
	if m := re.FindStringSubmatch(string(out)); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				var v float64
				fmt.Sscanf(g, "%f", &v)
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("timeout")
}
