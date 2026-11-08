// devboxen: the command line tool for your devboxen hosts and boxes. See the README.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dx-tooling/devboxen/internal/config"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

const usage = `devboxen: development boxes on your own servers.

Start:
  devboxen init [dir]                     set up devboxen step by step (a new devboxen.toml)
  devboxen host add [name]                add a server you have, or a new AWS host

Boxes:
  devboxen shell [box[@host]] [session]   open a shell in a box (tmux session, default "shell")
  devboxen claude [box[@host]] [name]     Claude Code in a box (session "claude" or "claude-<name>")
  devboxen list [host]                    boxes and their sessions (all hosts if none given)
  devboxen create box[@host] [--mem 8g] [--cpus 6]
  devboxen start box[@host]               start a stopped box
  devboxen stop box[@host]                stop a box (frees its memory; its data stays)
  devboxen destroy box[@host]             delete a box and all its data (asks first)

Logins and tokens:
  devboxen login [host]                   one Claude Code login for all boxes of a host
  devboxen github push [host]             send the GitHub tokens to the hosts (default: all running)
  devboxen github edit                    edit the encrypted GitHub tokens
  devboxen web passwd [user]              set the browser UI's password (then: devboxen deploy)

Hosts:
  devboxen host status|start|stop [host]  an on-demand cloud host
  devboxen host setup [host]              a new host: the box "main", the GitHub tokens
  devboxen costs [host] [--refresh]       what an on-demand cloud host costs
  devboxen deploy [host...] [--check] [--rebuild-boxes]
                                          set up or update the hosts and the browser UI

Other:
  devboxen tui                            all of the above as a menu
  devboxen secrets keygen                 make the age key pair for the encrypted secrets
  devboxen version

A box on another host than the default one: box@host, or @host for that host's default box.
Settings come from devboxen.toml (here or in a directory above; or $DEVBOXEN_CONFIG).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exit exitCode
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "devboxen: "+err.Error())
		os.Exit(1)
	}
}

// exitCode ends the program with that code and no message (e.g. the code of a remote command).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		fmt.Println("devboxen " + version)
		return nil
	}
	if cmd == "init" { // before there is a config
		return cmdInit(args)
	}

	path, err := config.Find()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	a := newApp(cfg)

	switch cmd {
	case "shell":
		return a.cmdShell(args)
	case "claude":
		return a.cmdClaude(args)
	case "list":
		return a.cmdList(args)
	case "create":
		return a.cmdCreate(args)
	case "start", "stop":
		return a.cmdStartStop(cmd, args)
	case "destroy":
		return a.cmdDestroy(args)
	case "login":
		return a.cmdLogin(args)
	case "github":
		return a.cmdGitHub(args)
	case "web":
		return a.cmdWeb(args)
	case "host":
		return a.cmdHost(args)
	case "costs":
		return a.cmdCosts(args)
	case "deploy":
		return a.cmdDeploy(args)
	case "secrets":
		return a.cmdSecrets(args)
	case "state":
		return a.cmdState(args)
	case "tui":
		return a.cmdTUI(args)
	}
	return fmt.Errorf("unknown command %q (devboxen help)", cmd)
}

func newApp(cfg *config.Config) *app {
	return &app{cfg: cfg,
		remoteDevbox: envOr("DEVBOXEN_REMOTE_DEVBOX", "devbox"),
		remoteAdmin:  envOr("DEVBOXEN_REMOTE_ADMIN", "devbox-admin"),
	}
}

type app struct {
	cfg          *config.Config
	remoteDevbox string // the host's `devbox` command
	remoteAdmin  string // the host's `devbox-admin` command
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// say prints a progress line.
func say(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "==> "+format+"\n", a...)
}

// target is a box on a host: "box@host", "@host" (that host's default box), "box" or "".
type target struct {
	box  string // "" = the host's default box
	host *config.Host
}

func (a *app) parseTarget(s string) (target, error) {
	box, host := s, ""
	if i := strings.LastIndex(s, "@"); i >= 0 {
		box, host = s[:i], s[i+1:]
	}
	h, err := a.cfg.Host(host)
	if err != nil {
		return target{}, err
	}
	if box != "" && !config.NameRE.MatchString(box) {
		return target{}, fmt.Errorf("no valid box name: %q (1-11 characters: a-z, 0-9 and -)", box)
	}
	return target{box: box, host: h}, nil
}

// name is box@host for messages, or box on the default host.
func (a *app) name(t target) string {
	box := t.box
	if box == "" {
		box = "(default box)"
	}
	if t.host.Name == a.cfg.DefaultHost {
		return box
	}
	return box + "@" + t.host.Name
}

// hostArg: a host argument, as "cloud" or "@cloud"; none = the default host.
func (a *app) hostArg(args []string) (*config.Host, error) {
	if len(args) == 0 {
		return a.cfg.Host("")
	}
	return a.cfg.Host(strings.TrimPrefix(args[0], "@"))
}

// splitFlags separates --name value options from positional arguments, wherever they are.
func splitFlags(args []string, valued map[string]bool) (pos []string, opts map[string]string, err error) {
	opts = map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" { // as task runners (mise) pass it along: no meaning here
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		takes, known := valued[name]
		if !known {
			return nil, nil, fmt.Errorf("unknown option --%s", name)
		}
		if takes && !hasValue {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		if !takes {
			value = "true"
		}
		opts[name] = value
	}
	return pos, opts, nil
}
