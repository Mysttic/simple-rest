package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// fakeSystem answers from maps: env holds the environment, files the paths
// that exist, and every directory in writable takes new files.
func fakeSystem(env map[string]string, files []string, writable ...string) system {
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	return system{
		getenv: func(k string) string { return env[k] },
		exists: func(p string) bool { return has(files, p) },
		glob: func(pattern string) []string {
			var out []string
			for _, f := range files {
				if ok, _ := filepath.Match(pattern, f); ok {
					out = append(out, f)
				}
			}
			return out
		},
		writable:  func(dir string) bool { return has(writable, dir) },
		configDir: func() (string, error) { return filepath.Join("home", "config"), nil },
	}
}

func TestDetectPackager(t *testing.T) {
	cases := []struct {
		exe   string
		env   map[string]string
		files []string
		want  string
	}{
		{`C:\Users\a\Downloads\simple-rest-1.3.0-windows-amd64.exe`, nil, nil, ""},
		{`C:\Users\a\scoop\apps\simple-rest\1.3.0\simple-rest.exe`, nil, nil, "Scoop"},
		{`D:\Tools\apps\simple-rest\current\simple-rest.exe`, map[string]string{"SCOOP": `D:\Tools\`}, nil, "Scoop"},
		{`D:\Tools\simple-rest.exe`, map[string]string{"SCOOP": `D:\Tools`}, nil, ""},
		{`C:\Users\a\AppData\Local\Microsoft\WinGet\Packages\Mysttic.SimpleRest_Microsoft.Winget.Source_8wekyb3d8bbwe\simple-rest.exe`, nil, nil, "winget"},
		{`C:\ProgramData\chocolatey\lib\simple-rest\tools\simple-rest.exe`, nil, nil, "Chocolatey"},
		{`E:\choco\lib\simple-rest\tools\simple-rest.exe`, map[string]string{"ChocolateyInstall": `E:\choco`}, nil, "Chocolatey"},
		{"/snap/simple-rest/12/simple-rest", map[string]string{"SNAP_NAME": "simple-rest"}, nil, "snap"},
		// A terminal inside another snap (VS Code) leaks SNAP_NAME.
		{"/home/a/bin/simple-rest", map[string]string{"SNAP_NAME": "code"}, nil, ""},
		{"/usr/bin/simple-rest", nil, []string{"/var/lib/dpkg/info/simple-rest.list"}, "deb"},
		{"/usr/bin/simple-rest", nil, []string{"/var/lib/pacman/local/simple-rest-bin-1.3.0-1"}, "AUR"},
		{"/usr/bin/simple-rest", nil, nil, "system"},
		{"/usr/local/bin/simple-rest", nil, nil, ""},
		{"/home/pi/simple-rest-1.3.0-linux-armhf", nil, nil, ""},
		{"", nil, nil, ""},
	}
	for _, c := range cases {
		got := detectPackager(c.exe, fakeSystem(c.env, c.files))
		if got.Name != c.want {
			t.Errorf("detectPackager(%q) = %q, want %q", c.exe, got.Name, c.want)
		}
		if (got.Name == "") != (got.Reason == "") {
			t.Errorf("detectPackager(%q): name %q with reason %q", c.exe, got.Name, got.Reason)
		}
	}
}

func TestDataFileFor(t *testing.T) {
	dir := filepath.Join("opt", "sr")
	exe := filepath.Join(dir, "simple-rest")
	beside := filepath.Join(dir, "simple-rest-data.json")
	inConfig := filepath.Join("home", "config", "simple-rest", "simple-rest-data.json")
	scoop := packager{Name: "Scoop", Reason: "x"}

	cases := []struct {
		name string
		pkg  packager
		sys  system
		want string
	}{
		{"downloaded copy, writable folder", packager{}, fakeSystem(nil, nil, dir), beside},
		{"downloaded copy, read-only folder", packager{}, fakeSystem(nil, nil), inConfig},
		{"downloaded copy, read-only folder with data", packager{}, fakeSystem(nil, []string{beside}), beside},
		{"package manager, writable folder", scoop, fakeSystem(nil, nil, dir), inConfig},
		{"package manager, data beside", scoop, fakeSystem(nil, []string{beside}, dir), beside},
		{"snap", packager{Name: "snap", Reason: "x"}, fakeSystem(map[string]string{"SNAP_USER_COMMON": "common"}, nil),
			filepath.Join("common", "simple-rest-data.json")},
	}
	for _, c := range cases {
		if got := dataFileFor(exe, c.pkg, c.sys); got != c.want {
			t.Errorf("%s: dataFileFor = %q, want %q", c.name, got, c.want)
		}
	}

	noConfig := fakeSystem(nil, nil)
	noConfig.configDir = func() (string, error) { return "", errors.New("no home") }
	if got := dataFileFor(exe, scoop, noConfig); got != beside {
		t.Errorf("without a config directory: dataFileFor = %q, want %q", got, beside)
	}
	if got := dataFileFor("", packager{}, noConfig); got != "simple-rest-data.json" {
		t.Errorf("unknown executable: dataFileFor = %q", got)
	}
}

func TestManagedByTurnsSelfUpdateOff(t *testing.T) {
	u := &updater{installable: true}
	u.managedBy(packager{})
	if !u.installable {
		t.Fatal("a downloaded copy lost self-update")
	}
	u.managedBy(packager{Name: "Scoop", Reason: "it was installed with Scoop"})
	if ok, why := u.canInstallLocked(); ok || why != "it was installed with Scoop" {
		t.Fatalf("canInstall = %v, %q", ok, why)
	}
}
