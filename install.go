// install.go: how this copy was installed and where it keeps its data.
//
// A copy started from a downloaded file keeps simple-rest-data.json next to
// itself and updates itself, as it always has. A copy that a package manager
// put in place does neither: next to the executable the data file would not
// survive the next upgrade (Scoop and Chocolatey replace the whole directory)
// or could not be written at all (/usr/bin, /snap), and replacing the
// executable is the package manager's job.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	releasesPage = "https://github.com/Mysttic/" + projectName + "/releases"
	wingetID     = "Mysttic.SimpleRest"
)

// packager is the package manager that installed this copy. Name is empty
// for a copy started from a downloaded file.
type packager struct {
	Name   string
	Reason string // why the copy does not update itself, shown in Settings
}

// system is what the detection reads, replaceable in tests.
type system struct {
	getenv    func(string) string
	exists    func(path string) bool
	glob      func(pattern string) []string
	writable  func(dir string) bool
	configDir func() (string, error)
}

func realSystem() system {
	return system{
		getenv: os.Getenv,
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		glob: func(p string) []string {
			m, _ := filepath.Glob(p)
			return m
		},
		writable:  dirWritable,
		configDir: os.UserConfigDir,
	}
}

// slashPath lowercases a path and turns its separators into slashes, so the
// same patterns match Windows and Unix paths on whatever system runs them.
func slashPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
}

// under reports whether the slashPath p lies in root/sub. An empty root,
// that is an unset environment variable, matches nothing.
func under(p, root, sub string) bool {
	if root == "" {
		return false
	}
	return strings.HasPrefix(p, strings.TrimRight(slashPath(root), "/")+"/"+sub+"/")
}

// detectPackager tells from the path of the executable (symlinks resolved)
// and the environment which package manager installed this copy. The checks
// look at where each manager puts its files, so a downloaded copy run from
// anywhere else is never mistaken for an installed one.
func detectPackager(exe string, s system) packager {
	if exe == "" {
		return packager{}
	}
	p := slashPath(exe)
	switch {
	case s.getenv("SNAP_NAME") == projectName && strings.HasPrefix(p, "/snap/"):
		return packager{"snap", "it came from the Snap Store, which updates it by itself"}
	case strings.Contains(p, "/scoop/apps/") || under(p, s.getenv("SCOOP"), "apps") || under(p, s.getenv("SCOOP_GLOBAL"), "apps"):
		return packager{"Scoop", "it was installed with Scoop, so update it with scoop update " + projectName}
	case strings.Contains(p, "/winget/packages/") || strings.Contains(p, "/winget/links/"):
		return packager{"winget", "it was installed with winget, so update it with winget upgrade " + wingetID}
	case strings.Contains(p, "/chocolatey/lib/") || under(p, s.getenv("ChocolateyInstall"), "lib"):
		return packager{"Chocolatey", "it was installed with Chocolatey, so update it with choco upgrade " + projectName}
	case strings.HasPrefix(p, "/usr/") && !strings.HasPrefix(p, "/usr/local/"):
		switch {
		case s.exists("/var/lib/dpkg/info/" + projectName + ".list"):
			return packager{"deb", "it was installed from a .deb package, so install the newer package from " + releasesPage}
		case len(s.glob("/var/lib/pacman/local/"+projectName+"-bin-*")) > 0:
			return packager{"AUR", "it was installed from the AUR, so update " + projectName + "-bin with your AUR helper"}
		}
		return packager{"system", "it was installed by the system's package manager, so update it there"}
	}
	return packager{}
}

// dataFileFor picks the data file when -data is not given. A data file that
// already lies next to the executable always wins, so no copy loses sight of
// its data. Otherwise a copy started from a downloaded file keeps the file next
// to itself whenever it can write there, and every other copy keeps it in the
// user's configuration directory: %AppData%\simple-rest on Windows,
// ~/.config/simple-rest on Linux, and the snap's own common directory
// (~/snap/simple-rest/common) in a snap.
func dataFileFor(exe string, pkg packager, s system) string {
	name := projectName + "-data.json"
	if exe == "" {
		return name
	}
	beside := filepath.Join(filepath.Dir(exe), name)
	if s.exists(beside) || (pkg.Name == "" && s.writable(filepath.Dir(exe))) {
		return beside
	}
	if dir := s.getenv("SNAP_USER_COMMON"); pkg.Name == "snap" && dir != "" {
		return filepath.Join(dir, name)
	}
	if dir, err := s.configDir(); err == nil && dir != "" {
		return filepath.Join(dir, projectName, name)
	}
	return beside
}
