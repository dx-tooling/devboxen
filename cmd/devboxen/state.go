package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dx-tooling/devboxen/internal/config"
)

// cmdState prints every host's boxes and sessions for scripts and menus, without asking
// anything (no AWS login, no host start). Tab-separated lines:
//
//	HOST KIND HOSTSTATE BOX STATUS MEMORY CPUS SESSIONS
//
// KIND is ondemand or fixed. HOSTSTATE is up, an EC2 state other than running (stopped,
// pending, …), login (the AWS login has expired), error or unreachable. SESSIONS is
// space-separated, a trailing * marks an attached one, - is none. A host whose boxes can't be
// listed, or that has none, gives one line with BOX -.
func (a *app) cmdState(args []string) error {
	if len(args) > 0 {
		return errors.New("devboxen state (no arguments)")
	}
	lines := make([][]string, len(a.cfg.Order))
	var wg sync.WaitGroup
	for i, name := range a.cfg.Order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines[i] = a.hostState(a.cfg.Hosts[name])
		}()
	}
	wg.Wait()
	for _, ls := range lines {
		for _, l := range ls {
			fmt.Println(l)
		}
	}
	return nil
}

func (a *app) hostState(h *config.Host) []string {
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
	none := []string{strings.Join([]string{h.Name, kind, hs, "-", "-", "-", "-", "-"}, "\t")}
	if hs != "up" {
		return none
	}
	// devbox-admin list: a header (BOX STATUS MEMORY CPUS SESSIONS), the boxes, a "(* = …)" footer.
	var res []string
	for i, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if i == 0 || len(f) < 4 || strings.HasPrefix(l, "(") {
			continue
		}
		var sessions []string
		for _, s := range f[4:] {
			if s != "-" {
				sessions = append(sessions, s)
			}
		}
		s := strings.Join(sessions, " ")
		if s == "" {
			s = "-"
		}
		res = append(res, strings.Join([]string{h.Name, kind, "up", f[0], f[1], f[2], f[3], s}, "\t"))
	}
	if len(res) == 0 {
		return none
	}
	return res
}
