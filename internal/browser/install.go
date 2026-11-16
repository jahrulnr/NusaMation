package browser

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallMethod is one way to get a Chromium executable on this host — either
// a self-contained download into the data dir (no root) or a package-manager
// install. The UI lists them as buttons; the CLI lists them for `browser
// install <id>`.
type InstallMethod struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"` // the exact command or download shown to the user
	Root   bool   `json:"needsRoot,omitempty"`
}

// InstallOffer describes how a host without a Chromium executable can get one.
// Methods are ordered by preference — the self-contained download first, then
// package managers. Missing=false means an executable already resolves.
type InstallOffer struct {
	Missing  bool            `json:"missing"`
	ExecPath string          `json:"execPath,omitempty"`
	Searched string          `json:"searched,omitempty"`
	Platform string          `json:"platform"`
	Proot    bool            `json:"proot,omitempty"`
	Methods  []InstallMethod `json:"methods,omitempty"`
}

// cftPlatforms maps GOOS/GOARCH onto Chrome for Testing download platforms.
// Google ships no linux/arm64 build — proot-distro on Android must go through
// the distro's package manager.
var cftPlatforms = map[string]string{
	"linux/amd64":   "linux64",
	"darwin/amd64":  "mac-x64",
	"darwin/arm64":  "mac-arm64",
	"windows/amd64": "win64",
	"windows/386":   "win32",
}

func cftPlatform() string { return cftPlatforms[runtime.GOOS+"/"+runtime.GOARCH] }

// pkgManagers are probed on PATH in order; the package names install a real
// Chromium on each distro family. On Ubuntu, `chromium` is a snap-transition
// stub — inside proot it installs but never runs, which the post-install
// resolve check reports honestly instead of claiming success.
var pkgManagers = []struct {
	id, bin, pkg, label string
}{
	{"apt", "apt-get", "chromium", "Debian / Ubuntu / proot-distro"},
	{"dnf", "dnf", "chromium", "Fedora / RHEL"},
	{"pacman", "pacman", "chromium", "Arch"},
	{"apk", "apk", "chromium", "Alpine"},
	{"zypper", "zypper", "chromium", "openSUSE"},
}

var lookPath = exec.LookPath

// ProotLike reports whether this process runs inside proot (Termux's
// proot-distro or similar): an Android kernel under a regular Linux userland,
// or proot's own env leaking in. Used to offer the right install method and to
// add the sandbox flags Chromium needs where user namespaces do not exist.
func ProotLike() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, k := range []string{"PROOT_L2S_DIR", "PROOT_TMP_DIR", "PROOT_LOADER", "PROOT_NO_SECCOMP"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	if strings.Contains(os.Getenv("LD_PRELOAD"), "proot") {
		return true
	}
	// The kernel belongs to the Android host — /etc/os-release still shows
	// the guest distro, so osrelease is the distinguishing signal.
	rel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(rel)), "android")
}

// Offer reports the resolution state plus the viable install methods.
// execPath is the -browser-exec override ("" = none); installRoot is where the
// self-contained download lands ("" = download method unavailable).
func Offer(execPath, installRoot string) InstallOffer {
	offer := InstallOffer{Platform: runtime.GOOS + "/" + runtime.GOARCH, Proot: ProotLike()}
	if p, err := resolveExec(execPath, installRoot); err == nil {
		offer.ExecPath = p
		return offer
	} else {
		offer.Missing = true
		offer.Searched = err.Error()
	}
	if plat := cftPlatform(); plat != "" && installRoot != "" {
		offer.Methods = append(offer.Methods, InstallMethod{
			ID:     "download",
			Label:  "Download Chrome for Testing headless shell",
			Detail: "chrome-headless-shell (" + plat + ") → " + installRoot,
		})
	}
	for _, pm := range pkgManagers {
		if _, err := lookPath(pm.bin); err == nil {
			offer.Methods = append(offer.Methods, InstallMethod{
				ID:     "pkg:" + pm.id,
				Label:  "Install " + pm.pkg + " via " + pm.bin,
				Detail: pm.bin + " install " + pm.pkg + " (" + pm.label + ")",
				Root:   true,
			})
		}
	}
	return offer
}

// Install runs one offered method and re-resolves the executable afterwards —
// the returned error is honest: a package manager can exit 0 yet leave no
// usable binary (Ubuntu's snap stub), so success is measured by what resolves,
// not by the installer exiting cleanly.
func Install(ctx context.Context, execPath, installRoot, method string, log io.Writer) (string, error) {
	switch {
	case method == "download":
		plat := cftPlatform()
		if plat == "" {
			return "", fmt.Errorf("no Chrome for Testing build for %s", runtime.GOOS+"/"+runtime.GOARCH)
		}
		if installRoot == "" {
			return "", errors.New("no install directory configured")
		}
		if err := installHeadlessShell(ctx, plat, installRoot, log); err != nil {
			return "", err
		}
	case strings.HasPrefix(method, "pkg:"):
		var pm *struct{ id, bin, pkg, label string }
		for i := range pkgManagers {
			if pkgManagers[i].id == strings.TrimPrefix(method, "pkg:") {
				pm = &pkgManagers[i]
			}
		}
		if pm == nil {
			return "", fmt.Errorf("unknown install method %q", method)
		}
		if _, err := lookPath(pm.bin); err != nil {
			return "", fmt.Errorf("%s is not on PATH", pm.bin)
		}
		if err := pkgInstall(ctx, pm.bin, pm.pkg, log); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown install method %q", method)
	}
	p, err := resolveExec(execPath, installRoot)
	if err != nil {
		return "", fmt.Errorf("the install ran but no Chromium executable resolves — %w", err)
	}
	fmt.Fprintf(log, "\nResolved executable: %s\n", p)
	return p, nil
}

// installHeadlessShell fetches the latest Stable chrome-headless-shell zip
// from the Chrome for Testing endpoints and extracts it under root. The
// extracted chrome-headless-shell-<plat>/ directory is probed by resolveExec.
func installHeadlessShell(ctx context.Context, plat, root string, log io.Writer) error {
	const meta = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	fmt.Fprintf(log, "Fetching %s\n", meta)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the Chrome for Testing index: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return fmt.Errorf("the Chrome for Testing index returned %s", resp.Status)
	}
	var doc struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads map[string][]struct {
				Platform string `json:"platform"`
				URL      string `json:"url"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&doc)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("bad Chrome for Testing index: %w", err)
	}
	url := ""
	for _, d := range doc.Channels["Stable"].Downloads["chrome-headless-shell"] {
		if d.Platform == plat {
			url = d.URL
		}
	}
	if url == "" {
		return fmt.Errorf("the index lists no chrome-headless-shell for %s", plat)
	}
	fmt.Fprintf(log, "Downloading %s\n", url)
	tmp, err := os.CreateTemp("", "nusamation-chrome-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err == nil {
		var zr *http.Response
		zr, err = http.DefaultClient.Do(req)
		if err == nil {
			if zr.StatusCode != http.StatusOK {
				err = fmt.Errorf("download failed: %s", zr.Status)
			} else {
				_, err = io.Copy(tmp, io.LimitReader(zr.Body, 512<<20))
			}
			_ = zr.Body.Close()
		}
	}
	_ = tmp.Close()
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	z, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return fmt.Errorf("the download is not a valid zip: %w", err)
	}
	defer z.Close()
	fmt.Fprintf(log, "Extracting to %s\n", root)
	for _, f := range z.File {
		target := filepath.Join(root, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(root)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes the install dir: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o755)
		if err == nil {
			_, err = io.Copy(out, rc)
			_ = out.Close()
		}
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// pkgInstall runs <bin> update then <bin> install -y <pkg> with output streamed
// to log. apt-get gets the noninteractive frontend so debconf cannot stall it.
func pkgInstall(ctx context.Context, bin, pkg string, log io.Writer) error {
	var steps [][]string
	switch bin {
	case "apt-get":
		steps = [][]string{{"update"}, {"install", "-y", pkg}}
	case "dnf", "zypper":
		steps = [][]string{{"install", "-y", pkg}}
	case "pacman":
		steps = [][]string{{"-Sy", "--noconfirm", pkg}}
	case "apk":
		steps = [][]string{{"add", pkg}}
	default:
		return fmt.Errorf("no install recipe for %s", bin)
	}
	for _, args := range steps {
		fmt.Fprintf(log, "$ %s %s\n", bin, strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
		}
	}
	return nil
}

// resolveExec is resolveExecPath's lookup without the Manager — CLI and the
// install runner share it so offers, installs and launches agree on what
// counts as found.
func resolveExec(override, installRoot string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, name := range execCandidates {
		if p, err := lookPath(name); err == nil {
			return p, nil
		}
	}
	searched := strings.Join(execCandidates, ", ")
	if fb := execFallbackPaths(); len(fb) > 0 {
		searched += ", standard install locations"
		for _, p := range fb {
			if isExecutableFile(p) {
				return p, nil
			}
		}
	}
	if installRoot != "" {
		searched += ", " + installRoot
		if matches, _ := filepath.Glob(filepath.Join(installRoot, "chrome-headless-shell-*", "chrome-headless-shell*")); len(matches) > 0 {
			for _, p := range matches {
				if isExecutableFile(p) {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w: no Chromium executable found (searched: %s); set -browser-exec or NUSAMATION_BROWSER_EXEC",
		ErrUnavailable, searched)
}

// isExecutableFile reports whether p is a file this platform can run. Windows
// permission bits are synthetic (0666 for a writable file), so an execute-bit
// test rejects every real executable there and made a successful download
// install resolve to nothing; the extension is what decides instead.
func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".exe", ".bat", ".cmd", ".com":
			return true
		}
		return false
	}
	return st.Mode()&0o111 != 0
}

// TailWriter keeps only the last max bytes — install logs can be megabytes of
// apt output and only the tail is interesting in a HTTP response or CLI exit.
type TailWriter struct {
	buf []byte
	max int
}

func NewTailWriter(max int) *TailWriter { return &TailWriter{max: max} }

func (w *TailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *TailWriter) String() string { return string(w.buf) }
