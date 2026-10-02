// Headless ai-bridge: same server as the app, for terminals and scripts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/skymanrm/ai-bridge/bridge"
)

const usage = `ai-bridge %s — use local Claude Code / Codex from web apps (headless)

Usage:
  ai-bridge serve [--port N]   run the HTTP bridge on 127.0.0.1 (default command)
  ai-bridge providers          list detected AI CLIs and their models
  ai-bridge token [--rotate]   print (or regenerate) the token web apps must send
  ai-bridge allow <origin>     allow a website, e.g. https://app.example.com
  ai-bridge deny <origin>      remove a website from the allowlist
  ai-bridge origins            list allowed websites
  ai-bridge version

Config: %s (shared with the AI Bridge app)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version", "--version":
		fmt.Println(bridge.Version)
		return nil
	case "help", "-h", "--help":
		path, _ := bridge.ConfigPath()
		fmt.Printf(usage, bridge.Version, path)
		return nil
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return err
	}
	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		port := fs.Int("port", cfg.Port, "listen port")
		_ = fs.Parse(args)
		cfg.Port = *port
		return bridge.NewServer(cfg, bridge.DefaultRegistry(cfg)).ListenAndServe()
	case "providers":
		infos := bridge.DefaultRegistry(cfg).Infos(context.Background(), true)
		out, _ := json.MarshalIndent(infos, "", "  ")
		fmt.Println(string(out))
		return nil
	case "token":
		if len(args) > 0 && args[0] == "--rotate" {
			cfg.Token = bridge.NewToken()
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Token rotated; paste the new one into your web apps.")
		}
		fmt.Println(cfg.Token)
		return nil
	case "allow", "deny":
		if len(args) != 1 {
			return fmt.Errorf("usage: ai-bridge %s <origin>", cmd)
		}
		apply, verb := cfg.AllowOrigin, "Allowed"
		if cmd == "deny" {
			apply, verb = cfg.DenyOrigin, "Removed"
		}
		origin, err := apply(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", verb, origin)
		return nil
	case "origins":
		if len(cfg.Origins) == 0 {
			fmt.Println("No origins allowed yet — run `ai-bridge allow <origin>`.")
		}
		fmt.Println(strings.Join(cfg.Origins, "\n"))
		return nil
	default:
		return errors.New("unknown command " + cmd + " (see ai-bridge help)")
	}
}
