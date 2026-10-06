package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/olivermarcusson/nebula/mods"
)

// Nebula's settings live in one file, <user config>/Nebula/config.json.
// Environment variables override it.
type config struct {
	ServerURL  string `json:"server_url,omitempty"`
	TokenFile  string `json:"token_file,omitempty"`
	Profiles   string `json:"profiles,omitempty"`
	ClaudePath string `json:"claude_path,omitempty"`
}

func configDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "Nebula")
}

func configFile() string { return filepath.Join(configDir(), "config.json") }

// loadConfig fills unset NEBULA_* variables from the config file.
func loadConfig() {
	raw, err := os.ReadFile(configFile())
	if err != nil {
		return
	}
	var c config
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	for k, v := range map[string]string{"NEBULA_SERVER_URL": c.ServerURL, "NEBULA_TOKEN_FILE": c.TokenFile, "NEBULA_PROFILES": c.Profiles, "NEBULA_CLAUDE_PATH": c.ClaudePath} {
		if v != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// binDir is where setup installs the companion and the launcher.
func binDir() string {
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
		return filepath.Join(local, "Programs", "Nebula")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Close()
	} else {
		out.Close()
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func setup(args []string) error {
	f := flags("setup")
	server := f.String("server", "", "Nebula server URL (https, or http on loopback)")
	tokenFile := f.String("token-file", "", "device token file to install")
	profiles := f.String("profiles", "", "extra Claude Code config directories to include")
	noService := f.Bool("no-service", false, "do not start the companion at login")
	noMod := f.Bool("no-mod", false, "do not install the Claude Code mod")
	uninstall := f.Bool("uninstall", false, "stop the companion service and remove the startup entry")
	if err := parse(f, args); err != nil {
		return err
	}
	if *uninstall {
		return removeService()
	}

	var c config
	if raw, err := os.ReadFile(configFile()); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	if *server != "" {
		c.ServerURL = strings.TrimRight(*server, "/")
	}
	if *profiles != "" {
		c.Profiles = *profiles
	}
	if c.ServerURL == "" {
		return errors.New("--server is required")
	}
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		return err
	}
	if *tokenFile != "" {
		dst := filepath.Join(configDir(), "device.token")
		if abs, _ := filepath.Abs(*tokenFile); abs != dst {
			if err := copyFile(*tokenFile, dst, 0600); err != nil {
				return fmt.Errorf("installing token: %w", err)
			}
		}
		c.TokenFile = dst
	}
	if c.TokenFile == "" {
		return errors.New("--token-file is required")
	}

	// Check the server and token before changing anything else.
	os.Setenv("NEBULA_SERVER_URL", c.ServerURL)
	os.Setenv("NEBULA_TOKEN_FILE", c.TokenFile)
	cl, err := newClient()
	if err != nil {
		return err
	}
	var me struct {
		Owner string `json:"owner"`
	}
	if err = cl.call(context.Background(), "GET", "/v1/me", nil, &me); err != nil {
		return fmt.Errorf("checking the server: %w", err)
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	if err = os.WriteFile(configFile(), raw, 0600); err != nil {
		return err
	}
	fmt.Printf("Connected to %s as %s.\n", c.ServerURL, me.Owner)

	// Install the binary and the launcher beside it.
	dir := binDir()
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	bin := filepath.Join(dir, exe("nebula"))
	if runtime.GOOS == "windows" && !*noService {
		stopWindowsCompanion() // a running .exe cannot be replaced
	}
	if s, _ := filepath.EvalSymlinks(self); s != bin {
		if err = copyFile(self, bin, 0755); err != nil {
			return fmt.Errorf("installing %s: %w", bin, err)
		}
	}
	launcher := filepath.Join(dir, exe("nebula-claude"))
	os.Remove(launcher)
	if runtime.GOOS == "windows" {
		err = copyFile(bin, launcher, 0755)
	} else {
		err = os.Symlink(bin, launcher)
	}
	if err != nil {
		return fmt.Errorf("installing the launcher: %w", err)
	}
	fmt.Println("Installed", bin, "and", launcher)

	if !*noService {
		if err = installService(bin); err != nil {
			fmt.Println("Could not start the companion at login:", err)
			fmt.Printf("Run it yourself: %s sync --watch\n", bin)
		}
	}
	if !*noMod {
		if err = installMod(bin); err != nil {
			fmt.Println("Could not install the Claude Code mod:", err)
		}
	}
	fmt.Println()
	fmt.Println("Next:")
	fmt.Printf("  - Start Claude with %s (or alias claude to it) to use the best connected account.\n", launcher)
	fmt.Printf("  - In T3 Code, set Settings > Claude > Binary path to %s.\n", launcher)
	fmt.Printf("  - Connect accounts from the dashboard (%s) or with: nebula login\n", c.ServerURL)
	return nil
}

// serviceName can be overridden to run a second companion (another server).
var serviceName = env("NEBULA_SERVICE_NAME", "nebula")

func installService(bin string) error {
	if runtime.GOOS == "windows" {
		return installStartup(bin)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemd is not available")
	}
	dir, _ := os.UserConfigDir()
	unit := filepath.Join(dir, "systemd", "user", serviceName+".service")
	if err := os.MkdirAll(filepath.Dir(unit), 0755); err != nil {
		return err
	}
	body := fmt.Sprintf(`[Unit]
Description=Nebula companion: session sync and Claude sign-in
After=network-online.target

[Service]
ExecStart=%s sync --watch
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, bin)
	if err := os.WriteFile(unit, []byte(body), 0644); err != nil {
		return err
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", serviceName + ".service"}, {"restart", serviceName + ".service"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %s", strings.Join(a, " "), strings.TrimSpace(string(out)))
		}
	}
	fmt.Printf("The companion runs in the background (systemctl --user status %s).\n", serviceName)
	return nil
}

// installStartup starts the companion hidden at Windows login, no admin needed.
func installStartup(bin string) error {
	script := startupScript()
	if err := os.MkdirAll(filepath.Dir(script), 0755); err != nil {
		return err
	}
	body := fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"\"\"%s\"\" sync --watch\", 0, False\r\n", bin)
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		return err
	}
	if err := exec.Command("wscript.exe", script).Start(); err != nil {
		return err
	}
	fmt.Println("The companion runs in the background and starts at login.")
	return nil
}

// stopWindowsCompanion ends background `nebula sync --watch` processes only.
func stopWindowsCompanion() {
	_ = exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process -Filter "Name='nebula.exe'" | Where-Object { $_.CommandLine -like '*sync --watch*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`).Run()
}

func startupScript() string {
	return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "nebula.vbs")
}

func removeService() error {
	if runtime.GOOS == "windows" {
		stopWindowsCompanion()
		if err := os.Remove(startupScript()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fmt.Println("Removed the startup entry.")
		return nil
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName+".service").Run()
	dir, _ := os.UserConfigDir()
	if err := os.Remove(filepath.Join(dir, "systemd", "user", serviceName+".service")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("Stopped and removed the companion service.")
	return nil
}

// installMod writes the embedded mod as a local marketplace, installs it with
// Claude Code's plugin manager, and points it at the companion.
func installMod(bin string) error {
	claude, err := realClaude()
	if err != nil {
		return err
	}
	market := filepath.Join(configDir(), "marketplace")
	_ = os.RemoveAll(market)
	err = fs.WalkDir(mods.Nebula, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := mods.Nebula.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(market, filepath.FromSlash(p))
		if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0644)
	})
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"name":    "nebula",
		"owner":   map[string]string{"name": "Nebula"},
		"plugins": []map[string]string{{"name": "nebula", "source": "./" + path.Clean("nebula")}},
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err = os.MkdirAll(filepath.Join(market, ".claude-plugin"), 0755); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(market, ".claude-plugin", "marketplace.json"), raw, 0644); err != nil {
		return err
	}
	run := func(args ...string) error {
		out, err := exec.Command(claude, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("claude %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
		return nil
	}
	_ = run("plugin", "marketplace", "remove", "nebula")
	if err = run("plugin", "marketplace", "add", market); err != nil {
		return err
	}
	_ = run("plugin", "uninstall", "nebula@nebula")
	if err = run("plugin", "install", "nebula@nebula"); err != nil {
		return err
	}
	if err = setClaudeEnv("NEBULA_COMPANION_PATH", bin); err != nil {
		return err
	}
	fmt.Println("Installed the Claude Code mod (/nebula-login, /nebula-sync, usage-limit switching).")
	return nil
}

// setClaudeEnv sets one variable in the home profile's settings.json "env",
// preserving everything else in the file.
func setClaudeEnv(key, value string) error {
	home, _ := homeProfile()
	file := filepath.Join(home, "settings.json")
	if real, err := filepath.EvalSymlinks(file); err == nil {
		file = real // write through a dotfiles symlink rather than replacing it
	}
	settings := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(file); err == nil {
		if err = json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON; leaving it alone", file)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	env := map[string]string{}
	if v, ok := settings["env"]; ok {
		if err := json.Unmarshal(v, &env); err != nil {
			return fmt.Errorf("%s has a non-string env; leaving it alone", file)
		}
	}
	env[key] = value
	settings["env"], _ = json.Marshal(env)
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	tmp := file + ".nebula-tmp"
	if err = os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
