package main

import (
	"os/exec"
	"strings"
)

const (
	screenSharingPlist = "/System/Library/LaunchDaemons/com.apple.screensharing.plist"
	sshPlist           = "/System/Library/LaunchDaemons/ssh.plist"
	kickstart          = "/System/Library/CoreServices/RemoteManagement/ARDAgent.app/Contents/Resources/kickstart"
)

func isLoaded(label string) bool {
	out, err := exec.Command("launchctl", "list", label).Output()
	return err == nil && strings.Contains(string(out), label)
}

func loadPlist(plist string) bool {
	return exec.Command("launchctl", "load", "-w", plist).Run() == nil
}

func unloadPlist(plist string) bool {
	return exec.Command("launchctl", "unload", "-w", plist).Run() == nil
}

func getConsoleUser() string {
	out, err := exec.Command("stat", "-f%Su", "/dev/console").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func EnableVNC() bool {
	consoleUser := getConsoleUser()
	if consoleUser != "" && consoleUser != "root" {
		// Sessão de usuário ativa — kickstart completo com restart do agente
		err := exec.Command(kickstart,
			"-activate", "-configure", "-access", "-on",
			"-restart", "-agent", "-privs", "-all",
		).Run()
		if err == nil {
			return true
		}
	}
	// Headless (sem login) ou kickstart falhou — ativa sem reiniciar agente de usuário
	err := exec.Command(kickstart,
		"-activate", "-configure", "-access", "-on",
		"-privs", "-all",
	).Run()
	if err != nil {
		return loadPlist(screenSharingPlist)
	}
	return true
}

func DisableVNC() bool {
	err := exec.Command(kickstart, "-deactivate", "-stop").Run()
	if err != nil {
		return unloadPlist(screenSharingPlist)
	}
	return true
}

func GetVNCStatus() bool { return isLoaded("com.apple.screensharing") }
func EnableSSH() bool    { return loadPlist(sshPlist) }
func DisableSSH() bool   { return unloadPlist(sshPlist) }
func GetSSHStatus() bool { return isLoaded("com.openssh.sshd") }

type ServicesStatus struct {
	VNC bool `json:"vnc"`
	SSH bool `json:"ssh"`
}

func GetAllServices() ServicesStatus {
	return ServicesStatus{VNC: GetVNCStatus(), SSH: GetSSHStatus()}
}
