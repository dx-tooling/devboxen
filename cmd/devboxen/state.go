package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dx-tooling/devboxen/internal/config"
)

// boxRow is one box of a host, or a host without listable boxes (box "-").
type boxRow struct {
	host      string
	kind      string // ondemand or fixed
	hostState string // up, an EC2 state other than running, login, error or unreachable
	box       string
	status    string // container state or VM state (exited, kvm-off, libvirt-unavailable, …)
	mem, cpus string
	boxKind   string   // container or vm (docs/vm.md); empty from a host older than VM boxes
	sessions  []string // a trailing * marks one with a terminal attached
}

// hostsState looks at every host at once, without asking anything (no AWS login, no host
// start).
func (a *app) hostsState() []boxRow {
	rows := make([][]boxRow, len(a.cfg.Order))
	var wg sync.WaitGroup
	for i, name := range a.cfg.Order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = a.hostRows(a.cfg.Hosts[name])
		}()
	}
	wg.Wait()
	var all []boxRow
	for _, r := range rows {
		all = append(all, r...)
	}
	return all
}

func (a *app) hostRows(h *config.Host) []boxRow {
	kind, hs := "fixed", "up"
	if h.OnDemand() {
		kind = "ondemand"
		inst, err := describe(h, false)
		switch {
		case errors.Is(err, errLogin):
			hs = "login"
		case err != nil:
			hs = "error"
		case inst.State != "running":
			hs = inst.State
		}
	}
	var out string
	if hs == "up" {
		var err error
		if out, err = a.adminOutput(h, 8, "list"); err != nil {
			hs = "unreachable"
		}
	}
	none := []boxRow{{host: h.Name, kind: kind, hostState: hs, box: "-"}}
	if hs != "up" {
		return none
	}
	res := parseList(h.Name, kind, out)
	if len(res) == 0 {
		return none
	}
	return res
}

// parseList reads devbox-admin list: a header (BOX STATUS MEMORY CPUS [KIND] SESSIONS), the
// boxes, a "(* = …)" footer. KIND came with VM boxes; a host without it lists containers.
func parseList(host, hostKind, out string) []boxRow {
	var res []boxRow
	sessionsAt := 4
	for i, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if i == 0 {
			if len(f) > 4 && f[4] == "KIND" {
				sessionsAt = 5
			}
			continue
		}
		if len(f) < 4 || strings.HasPrefix(l, "(") {
			continue
		}
		r := boxRow{host: host, kind: hostKind, hostState: "up", box: f[0], status: f[1], mem: f[2], cpus: f[3], boxKind: "container"}
		if sessionsAt == 5 && len(f) > 4 {
			r.boxKind = f[4]
		}
		if len(f) > sessionsAt {
			for _, s := range f[sessionsAt:] {
				if s != "-" {
					r.sessions = append(r.sessions, s)
				}
			}
		}
		res = append(res, r)
	}
	return res
}

// cmdState prints every host's boxes and sessions for scripts, without asking anything.
// Tab-separated lines:
//
//	HOST KIND HOSTSTATE BOX STATUS MEMORY CPUS SESSIONS BOXKIND
//
// SESSIONS is space-separated, a trailing * marks an attached one, - is none. A ninth column
// BOXKIND (container or vm) follows. A host whose boxes can't be listed, or that has none,
// gives one line with BOX and the rest -.
func (a *app) cmdState(args []string) error {
	if len(args) > 0 {
		return errors.New("devboxen state (no arguments)")
	}
	for _, r := range a.hostsState() {
		dash := func(s string) string {
			if s == "" {
				return "-"
			}
			return s
		}
		fmt.Println(strings.Join([]string{r.host, r.kind, r.hostState, r.box, dash(r.status),
			dash(r.mem), dash(r.cpus), dash(strings.Join(r.sessions, " ")), dash(r.boxKind)}, "\t"))
	}
	return nil
}
