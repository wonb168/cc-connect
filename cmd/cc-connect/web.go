package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func runWeb(args []string) {
	if len(args) > 0 && args[0] == "adduser" {
		runWebAddUser(args[1:])
		return
	}
	configPath := resolveConfigPath("")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\nRun cc-connect first to create a default config.\n", configPath)
		os.Exit(1)
	}

	// Use LoadPermissive so `cc-connect web` works even before any platforms
	// are configured (e.g. during initial setup via the Web Admin UI).
	cfg, err := config.LoadPermissive(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	config.ConfigPath = configPath

	mgmtEnabled := cfg.Management.Enabled != nil && *cfg.Management.Enabled
	port := cfg.Management.Port
	if port == 0 {
		port = 9820
	}
	token := cfg.Management.Token

	if !mgmtEnabled {
		fmt.Println("Web admin is not enabled. Configuring...")

		mgmtToken := core.GenerateToken(16)
		bridgeToken := core.GenerateToken(16)
		result, err := config.EnableWebAdmin(mgmtToken, bridgeToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling web admin: %v\n", err)
			os.Exit(1)
		}
		port = result.ManagementPort
		token = result.ManagementToken
		fmt.Printf("Web admin configured on port %d.\n", port)
		fmt.Println("Restart cc-connect for the changes to take effect.")
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	noBrowser := false
	for _, a := range args {
		if a == "--no-browser" || a == "-n" {
			noBrowser = true
		}
	}

	if noBrowser {
		fmt.Printf("URL:   %s\n", baseURL)
		fmt.Printf("Token: %s\n", token)
		return
	}

	loginURL := fmt.Sprintf("%s/login?token=%s",
		baseURL, url.QueryEscape(token))

	fmt.Printf("Opening: %s\n", baseURL)
	if err := openBrowser(loginURL); err != nil {
		fmt.Printf("\nCould not open browser automatically.\n")
		fmt.Printf("Open this URL in your browser:\n")
		fmt.Printf("  %s/login?token=%s\n", baseURL, token)
		fmt.Printf("\nNote: make sure cc-connect is running (it hosts the web admin on port %d).\n", port)
	}
}

// runWebAddUser implements `cc-connect web adduser <name> [--role viewer]`.
// It hashes the password with bcrypt and writes the account into config.toml
// so that nobody has to hand-craft a hash.
func runWebAddUser(args []string) {
	name := ""
	role := config.ManagementRoleAdmin
	explicitConfig := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--role", "-r":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--role requires a value (admin or viewer)")
				os.Exit(1)
			}
			i++
			role = strings.ToLower(args[i])
		case "--config", "-c":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--config requires a path")
				os.Exit(1)
			}
			i++
			explicitConfig = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
			if name != "" {
				fmt.Fprintln(os.Stderr, "Only one user name may be given")
				os.Exit(1)
			}
			name = args[i]
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "Usage: cc-connect web adduser <name> [--role admin|viewer] [--config <path>]")
		os.Exit(1)
	}
	if role != config.ManagementRoleAdmin && role != config.ManagementRoleViewer {
		fmt.Fprintf(os.Stderr, "Invalid role %q (want admin or viewer)\n", role)
		os.Exit(1)
	}

	configPath := resolveConfigPath(explicitConfig)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", configPath)
		os.Exit(1)
	}
	config.ConfigPath = configPath

	password, err := promptPassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading password: %v\n", err)
		os.Exit(1)
	}

	hash, err := core.HashManagementPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing password: %v\n", err)
		os.Exit(1)
	}

	updated, err := config.AddManagementUser(name, hash, role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving user: %v\n", err)
		os.Exit(1)
	}
	verb := "Added"
	if updated {
		verb = "Updated"
	}
	fmt.Printf("%s user %q with role %q in %s\n", verb, name, role, configPath)
	fmt.Println("Restart cc-connect for the change to take effect.")
}

// promptPassword reads a password twice without echoing it.
func promptPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Non-interactive (pipe/CI): read a single line from stdin.
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", fmt.Errorf("empty password")
		}
		return pw, nil
	}

	fmt.Print("Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	fmt.Print("Confirm password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", fmt.Errorf("passwords do not match")
	}
	if len(first) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	return string(first), nil
}

func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "linux":
		if isWSL() {
			return exec.Command("cmd.exe", "/c", "start", rawURL).Start()
		}
		// On headless Linux, xdg-open is often unavailable.
		// Check early and return a clear error so the caller can print the URL.
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("xdg-open not found (headless server?): %w", err)
		}
		return exec.Command("xdg-open", rawURL).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", rawURL).Start()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}
