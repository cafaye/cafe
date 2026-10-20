package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// `cafe tunnel` manages Cafaye-managed Cloudflare tunnels for a project's dev box.
//
// The tunnel is created via ship (which provisions it on Cloudflare and returns
// a token). `start` then runs cloudflared as a systemd user service so the dev
// server on the box is reachable at the tunnel URL without keeping a terminal open.

type tunnelDeps = shipDeps

func newTunnelCommand(deps tunnelDeps) *Command {
	deps, _ = deps.withDefaults()
	c := &Command{
		Name:    "tunnel",
		Summary: "manage dev-box tunnels",
		Usage:   "cafe tunnel <subcommand> [args]",
		LongHelp: "Manage Cafaye-managed Cloudflare tunnels for a project.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>         Create a tunnel and print its token/URL\n" +
			"  start <project-id>          Create (if needed) and start cloudflared as a service\n" +
			"  stop                       Stop the cloudflared service\n" +
			"  status                     Show service status\n\n" +
			"Environment:\n" +
			"  SHIP_URL    Base URL of the ship service\n" +
			"  SHIP_TOKEN  Service token\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe tunnel: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return tunnelCreate(deps, env, args[1:])
		case "start":
			return tunnelStart(deps, env, args[1:])
		case "stop":
			return tunnelStop(env, args[1:])
		case "status":
			return tunnelStatus(env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe tunnel: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func tunnelCreate(deps shipDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: cafe tunnel create: missing project ID", errUsage)
	}
	projectID := args[0]
	req2, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/"+projectID+"/tunnel", map[string]any{"box_number": 1})
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req2)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func tunnelStart(deps shipDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("tunnel start", flag.ContinueOnError)
	port := fs.Int("port", 3000, "local port to expose")
	if err := shipParse(fs, args, "tunnel", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) < 1 {
		return fmt.Errorf("%w: cafe tunnel start: missing project ID", errUsage)
	}
	projectID := fs.Args()[0]

	// Create tunnel via ship
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/"+projectID+"/tunnel", map[string]any{"box_number": 1})
	if err != nil {
		return err
	}
	respBody, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var t struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(respBody, &t); err != nil {
		return fmt.Errorf("parse tunnel response: %w", err)
	}
	if t.Token == "" {
		return fmt.Errorf("no tunnel token returned")
	}

	// Write token to file
	tokenDir := filepath.Join(os.Getenv("HOME"), ".config", "cafaye")
	os.MkdirAll(tokenDir, 0700)
	tokenFile := filepath.Join(tokenDir, "tunnel-token")
	os.WriteFile(tokenFile, []byte(t.Token), 0600)

	// Create systemd user service
	serviceDir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	os.MkdirAll(serviceDir, 0755)
	serviceFile := filepath.Join(serviceDir, "cafaye-tunnel.service")
	serviceContent := fmt.Sprintf(`[Unit]
Description=Cafaye dev tunnel
After=network.target

[Service]
ExecStart=/usr/local/bin/cloudflared tunnel run --token %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, t.Token)
	os.WriteFile(serviceFile, []byte(serviceContent), 0644)

	// Reload and start
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "cafaye-tunnel.service").Run()
	out, err := exec.Command("systemctl", "--user", "restart", "cafaye-tunnel.service").CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "systemctl: %s\n", string(out))
	}

	_, _ = fmt.Fprintf(env.Stdout, "tunnel started\n  url:  %s\n  port: %d (configure your app to listen here)\n  service: cafaye-tunnel.service\n", t.URL, *port)
	return nil
}

func tunnelStop(env *Env, args []string) error {
	out, err := exec.Command("systemctl", "--user", "stop", "cafaye-tunnel.service").CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "systemctl: %s\n", string(out))
		return err
	}
	_, _ = fmt.Fprintln(env.Stdout, "tunnel stopped")
	return nil
}

func tunnelStatus(env *Env, args []string) error {
	out, err := exec.Command("systemctl", "--user", "status", "cafaye-tunnel.service").CombinedOutput()
	_, _ = fmt.Fprint(env.Stdout, string(out))
	if err != nil {
		return nil // status returns non-zero when inactive
	}
	return nil
}

