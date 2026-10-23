//go:build windows

package win

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// SystemSummary is the one-line description of the machine that every bug
// report needs and nobody remembers to include: OS build, architecture,
// elevation. It is logged at startup and included in diagnostics.
func SystemSummary() string {
	return fmt.Sprintf("%s %s (%s), %s, %s",
		osName(), osDisplayVersion(), osBuild(), runtime.GOARCH, elevationText())
}

// SystemUsesDarkTheme reports whether Windows is set to the dark theme.
//
// The interface can follow the system theme, and the window frame has to
// agree with it: a dark frame around a light interface is the same
// mistake the default caption was.
func SystemUsesDarkTheme() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer key.Close()
	light, _, err := key.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return true
	}
	return light == 0
}

// OSName returns the product name, e.g. "Windows 11 Pro".
func OSName() string { return osName() }

// OSBuild returns the build number, e.g. "26100.1742".
func OSBuild() string { return osBuild() }

func elevationText() string {
	if IsElevated() {
		return "elevated"
	}
	return "not elevated"
}

// ElevationText is the human form of the elevation state, used in the UI.
func ElevationText() string { return elevationText() }

func currentVersionKey() (registry.Key, error) {
	return registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
}

func osName() string {
	k, err := currentVersionKey()
	if err != nil {
		return "Windows"
	}
	defer k.Close()

	// ProductName still says "Windows 10" on Windows 11 unless the manifest
	// targets 11, so the build number decides.
	name, _, _ := k.GetStringValue("ProductName")
	build := osBuildNumber()
	if build >= 22000 && strings.Contains(name, "Windows 10") {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if name == "" {
		name = "Windows"
	}
	return name
}

func osDisplayVersion() string {
	k, err := currentVersionKey()
	if err != nil {
		return "unknown"
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("DisplayVersion"); err == nil && v != "" {
		return v
	}
	if v, _, err := k.GetStringValue("ReleaseId"); err == nil && v != "" {
		return v
	}
	return "unknown"
}

func osBuildNumber() int {
	k, err := currentVersionKey()
	if err != nil {
		return 0
	}
	defer k.Close()
	build, _, err := k.GetStringValue("CurrentBuildNumber")
	if err != nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(build, "%d", &n)
	return n
}

func osBuild() string {
	k, err := currentVersionKey()
	if err != nil {
		return "build unknown"
	}
	defer k.Close()
	build, _, _ := k.GetStringValue("CurrentBuildNumber")
	if ubr, _, err := k.GetIntegerValue("UBR"); err == nil && build != "" {
		return fmt.Sprintf("build %s.%d", build, ubr)
	}
	if build == "" {
		return "build unknown"
	}
	return "build " + build
}

// ExePath is the running executable, logged so a report can be tied to a
// specific build and location.
func ExePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
