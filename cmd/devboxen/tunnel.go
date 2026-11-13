package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type boxPort struct {
	port   int
	bind   string
	owner  string // process name, or the name of the container publishing this port
	docker bool
}

var ssProcessRE = regexp.MustCompile(`users:\(\("([^"]+)"`)

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid TCP port %q (use 1–65535)", s)
	}
	return p, nil
}

// ss reports listeners in the box's own network namespace. This also sees ports that
// its nested Docker publishes on 127.0.0.1, which the host cannot reach via the box IP.
func parsePorts(out string) []boxPort {
	var ports []boxPort
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "LISTEN" {
			continue
		}
		i := strings.LastIndex(fields[3], ":")
		if i < 0 {
			continue
		}
		addr, ps := fields[3][:i], fields[3][i+1:]
		p, err := parsePort(ps)
		if err != nil {
			continue
		}
		addr = strings.Trim(addr, "[]")
		key := fmt.Sprintf("%d/%s", p, addr)
		if !seen[key] {
			owner := ""
			if match := ssProcessRE.FindStringSubmatch(line); len(match) == 2 {
				owner = match[1]
			}
			ports = append(ports, boxPort{port: p, bind: addr, owner: owner})
			seen[key] = true
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].port != ports[j].port {
			return ports[i].port < ports[j].port
		}
		return ports[i].bind < ports[j].bind
	})
	return ports
}

// docker inspect reports the published host bindings as structured data. Its names
// replace docker-proxy in ss: that process owns the socket, but the container is what
// the user needs to recognize. Add bindings even when Docker uses NAT without a proxy.
func addDockerPorts(ports []boxPort, out string) []boxPort {
	bySocket := map[string]int{}
	for i, p := range ports {
		bySocket[fmt.Sprintf("%d/%s", p.port, p.bind)] = i
	}
	for _, line := range strings.Split(out, "\n") {
		name, raw, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, "/") {
			continue
		}
		var bindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}
		if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
			continue
		}
		for containerPort, published := range bindings {
			if !strings.HasSuffix(containerPort, "/tcp") {
				continue
			}
			for _, b := range published {
				port, err := parsePort(b.HostPort)
				if err != nil {
					continue
				}
				bind := b.HostIP
				if bind == "" {
					bind = "0.0.0.0"
				}
				key := fmt.Sprintf("%d/%s", port, bind)
				if i, ok := bySocket[key]; ok {
					ports[i].owner, ports[i].docker = strings.TrimPrefix(name, "/"), true
				} else {
					bySocket[key] = len(ports)
					ports = append(ports, boxPort{port: port, bind: bind, owner: strings.TrimPrefix(name, "/"), docker: true})
				}
			}
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].port != ports[j].port {
			return ports[i].port < ports[j].port
		}
		return ports[i].bind < ports[j].bind
	})
	return ports
}

const dockerPortMarker = "__DEVBOXEN_DOCKER_PORTS__"

const boxPortSurvey = `sudo ss -H -ltnp || exit; printf '\n__DEVBOXEN_DOCKER_PORTS__\n'; docker ps -q | xargs -r docker inspect --format '{{.Name}} {{json .NetworkSettings.Ports}}'`

func (a *app) boxPorts(t target) ([]boxPort, error) {
	if err := a.ensureUp(t.host, true); err != nil {
		return nil, err
	}
	args := []string{a.remoteDevbox}
	if t.box != "" {
		args = append(args, "-b", t.box)
	}
	args = append(args, "--", "sh", "-c", boxPortSurvey)
	cmd := sshCmd(t.host, []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, remoteCommand(args...))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing ports in %s: %s", a.name(t), sshFailure(err, stderr.String()))
	}
	ss, docker, ok := strings.Cut(string(out), dockerPortMarker)
	if !ok {
		return nil, errors.New("port survey did not return Docker port data")
	}
	return addDockerPorts(parsePorts(ss), docker), nil
}

func sshFailure(err error, stderr string) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	return err.Error()
}

func (a *app) cmdPorts(args []string) error {
	if len(args) > 1 {
		return errors.New("devboxen ports [box[@host]]")
	}
	t, err := a.parseTarget(first(args))
	if err != nil {
		return err
	}
	ports, err := a.boxPorts(t)
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		fmt.Println("No TCP listeners in the box.")
		return nil
	}
	fmt.Println("PORT   BIND             OWNER")
	for _, p := range ports {
		fmt.Printf("%-6d %-16s %s\n", p.port, p.bind, portOwner(p))
	}
	return nil
}

func portOwner(p boxPort) string {
	if p.owner == "" {
		return "-"
	}
	if p.docker {
		return "container: " + p.owner
	}
	return "process: " + p.owner
}

// The relay runs inside the box and connects to the selected bind address. stdin/stdout are
// raw bytes, so TLS, WebSockets and HTTP/2 pass through without interpretation.
const boxTCPRelay = `const net=require("node:net");const s=net.connect({host:process.argv[1],port:Number(process.argv[2])});process.stdin.pipe(s);s.pipe(process.stdout);s.on("error",e=>{console.error(e.message);process.exitCode=1})`

func relayAddress(bind string) (string, error) {
	switch bind {
	case "", "0.0.0.0", "*", "127.0.0.1":
		return "127.0.0.1", nil
	case "::", "::1":
		return "::1", nil
	}
	if net.ParseIP(bind) == nil {
		return "", fmt.Errorf("invalid box bind address %q", bind)
	}
	return bind, nil
}

func (a *app) cmdTunnel(args []string) error {
	pos, opts, err := splitFlags(args, map[string]bool{"local-port": true, "remote-addr": true})
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("devboxen tunnel [box[@host]] port [--local-port port] [--remote-addr IP]")
	}
	box, remote := "", pos[0]
	if len(pos) == 2 {
		box, remote = pos[0], pos[1]
	}
	t, err := a.parseTarget(box)
	if err != nil {
		return err
	}
	port, err := parsePort(remote)
	if err != nil {
		return err
	}
	local := port
	if v := opts["local-port"]; v != "" {
		local, err = parsePort(v)
		if err != nil {
			return err
		}
	}
	addr, err := relayAddress(opts["remote-addr"])
	if err != nil {
		return err
	}
	if err := a.ensureUp(t.host, true); err != nil {
		return err
	}
	box, err = a.runningBoxName(t)
	if err != nil {
		return err
	}
	t.box = box
	return a.tunnel(t, port, local, addr)
}

// Resolve the same default as the host's devbox command, once before opening the
// listener. The stream itself enters that container directly: the devbox wrapper's
// preflight docker exec used to read from stdin and could eat the first TLS packet.
func (a *app) runningBoxName(t target) (string, error) {
	out, err := a.adminOutput(t.host, 10, "list", "--names")
	if err != nil {
		return "", err
	}
	names := strings.Fields(out)
	if t.box != "" {
		for _, name := range names {
			if name == t.box {
				return name, nil
			}
		}
		return "", fmt.Errorf("no such devbox: %s", t.box)
	}
	for _, name := range names {
		if name == "main" {
			return name, nil
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	if len(names) == 0 {
		return "", errors.New("there is no devbox yet")
	}
	return "", errors.New("several devboxes and none called main; select a box")
}

func (a *app) tunnel(t target, port, local int, addr string) error {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", local))
	if err != nil {
		return fmt.Errorf("cannot bind localhost port %d: %w (try --local-port)", local, err)
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); listener.Close() }()
	fmt.Printf("Forwarding %s port %d to localhost:%d\n", a.name(t), port, local)
	fmt.Printf("Open http://localhost:%d/ or https://localhost:%d/ as your app requires. Ctrl-C stops the tunnel.\n", local, local)
	fmt.Println("For mkcert HTTPS, this computer must trust the box's mkcert CA (see docs/box.md).")
	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			a.relayConnection(ctx, t, port, addr, conn)
		}()
	}
	wg.Wait()
	return nil
}

func (a *app) relayConnection(ctx context.Context, t target, port int, addr string, conn net.Conn) {
	args := []string{"docker", "exec", "-i", "-u", "dev", "-w", "/home/dev", "devbox-" + t.box,
		"node", "-e", boxTCPRelay, addr, strconv.Itoa(port)}
	sshArgs := append(append([]string{}, sshBase...), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", t.host.SSH, remoteCommand(args...))
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin, cmd.Stdout, cmd.Stderr = conn, conn, os.Stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Tunnel connection to %s:%d ended: %v\n", a.name(t), port, err)
	}
}
