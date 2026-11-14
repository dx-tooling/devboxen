package main

import (
	"strings"
	"testing"
)

func TestParsePorts(t *testing.T) {
	out := `LISTEN 0 4096 127.0.0.1:32795 0.0.0.0:* users:(("docker-proxy",pid=215193,fd=7))
LISTEN 0 128 0.0.0.0:8000 0.0.0.0:*
LISTEN 0 128 [::1]:8443 [::]:*
LISTEN 0 128 [::]:8000 [::]:*
LISTEN 0 128 127.0.0.1:32795 0.0.0.0:*
LISTEN 0 128 127.0.0.1:43395 0.0.0.0:* users:(("codex",pid=8693,fd=33))
LISTEN 0 128 127.0.0.1:* 0.0.0.0:*
`
	got := parsePorts(out)
	want := []boxPort{
		{port: 8000, bind: "0.0.0.0"}, {port: 8000, bind: "::"},
		{port: 8443, bind: "::1"}, {port: 32795, bind: "127.0.0.1", owner: "docker-proxy"},
		{port: 43395, bind: "127.0.0.1", owner: "codex"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDockerPortOwners(t *testing.T) {
	ports := parsePorts(`LISTEN 0 4096 127.0.0.1:32795 0.0.0.0:* users:(("docker-proxy",pid=215193,fd=7))
LISTEN 0 128 127.0.0.1:43395 0.0.0.0:* users:(("codex",pid=8693,fd=33))`)
	docker := `/luminor_meridian-external-agent-harness_nginx {"443/tcp":[{"HostIp":"127.0.0.1","HostPort":"32795"}],"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"32794"}]}
/irrelevant {"443/udp":[{"HostIp":"127.0.0.1","HostPort":"43395"}],"9000/tcp":null}
`
	got := addDockerPorts(ports, docker)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].port != 32794 || !got[0].docker || got[0].owner != "luminor_meridian-external-agent-harness_nginx" ||
		portOwner(got[0]) != "container: luminor_meridian-external-agent-harness_nginx → 80 (HTTP)" {
		t.Errorf("NAT-only Docker binding: %+v", got[0])
	}
	if got[1].port != 32795 || portOwner(got[1]) != "container: luminor_meridian-external-agent-harness_nginx → 443 (HTTPS)" {
		t.Errorf("docker-proxy owner: %+v", got[1])
	}
	if got[2].port != 43395 || portOwner(got[2]) != "process: codex" {
		t.Errorf("process owner: %+v", got[2])
	}
}

func TestTunnelOptions(t *testing.T) {
	for _, bad := range []string{"0", "65536", "-1", "http", "32795x"} {
		if _, err := parsePort(bad); err == nil {
			t.Errorf("accepted port %q", bad)
		}
	}
	for in, want := range map[string]string{"": "127.0.0.1", "*": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "::1", "::1": "::1", "172.18.0.2": "172.18.0.2"} {
		got, err := relayAddress(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := relayAddress("localhost; whoami"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("unsafe address accepted: %v", err)
	}
}
