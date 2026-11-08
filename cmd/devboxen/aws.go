package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dx-tooling/devboxen/internal/config"
)

// On-demand hosts are EC2 instances, driven with the AWS CLI and the profile from
// devboxen.toml. An expired SSO login is renewed (`aws sso login`, in the browser) when a
// command needs AWS, never while only looking (state for menus).

var loginNeeded = regexp.MustCompile(`(?i)sso|token|expired|credentials`)

// errLogin: the AWS login has expired.
var errLogin = errors.New("the AWS login has expired")

func awsArgs(h *config.Host, region string, args ...string) []string {
	if region == "" {
		region = h.AWS.Region
	}
	return append([]string{"--profile", h.AWS.Profile, "--region", region}, args...)
}

// awsRun runs the AWS CLI and returns its output; errLogin if the login has expired.
func awsRun(h *config.Host, region string, args ...string) (string, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return "", fmt.Errorf("the host %s needs the AWS CLI on this computer (https://aws.amazon.com/cli/)", h.Name)
	}
	cmd := exec.Command("aws", awsArgs(h, region, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if loginNeeded.MatchString(msg) {
			return "", errLogin
		}
		return "", fmt.Errorf("AWS: %s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// awsLogin renews the SSO login (in the browser).
func awsLogin(h *config.Host) error {
	args := []string{"sso", "login"}
	what := "profile " + h.AWS.Profile
	if h.AWS.SSOSession != "" {
		args = append(args, "--sso-session", h.AWS.SSOSession)
		what = "SSO session " + h.AWS.SSOSession
	} else {
		args = append(args, "--profile", h.AWS.Profile)
	}
	say("logging in to AWS (%s; confirm in the browser)", what)
	cmd := exec.Command("aws", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	return cmd.Run()
}

// awsDo: awsRun, logging in once if the login has expired.
func awsDo(h *config.Host, region string, args ...string) (string, error) {
	out, err := awsRun(h, region, args...)
	if errors.Is(err, errLogin) {
		if err := awsLogin(h); err != nil {
			return "", err
		}
		out, err = awsRun(h, region, args...)
	}
	return out, err
}

type instance struct {
	State  string
	Type   string
	IP     string
	Launch time.Time
}

func describe(h *config.Host, login bool) (instance, error) {
	run := awsRun
	if login {
		run = awsDo
	}
	out, err := run(h, "", "ec2", "describe-instances", "--instance-ids", h.AWS.Instance, "--output", "json",
		"--query", "Reservations[0].Instances[0].{State:State.Name,Type:InstanceType,IP:PublicIpAddress,Launch:LaunchTime}")
	if err != nil {
		return instance{}, err
	}
	var raw struct{ State, Type, IP, Launch string }
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return instance{}, fmt.Errorf("AWS: unexpected answer: %s", out)
	}
	i := instance{State: raw.State, Type: raw.Type, IP: raw.IP}
	i.Launch, _ = time.Parse(time.RFC3339, raw.Launch)
	return i, nil
}

// ensureUp makes sure a host can be reached. A fixed host always can. A stopped on-demand
// host is started (start), or reported (!start: a reconnect must not start a host that was
// stopped on purpose).
func (a *app) ensureUp(h *config.Host, start bool) error {
	if !h.OnDemand() {
		return nil
	}
	inst, err := describe(h, true)
	if err != nil {
		return err
	}
	switch inst.State {
	case "running":
		return nil
	case "stopping":
		say("waiting for the host %s to finish stopping", h.Name)
		if _, err := awsDo(h, "", "ec2", "wait", "instance-stopped", "--instance-ids", h.AWS.Instance); err != nil {
			return err
		}
		fallthrough
	case "stopped":
		if !start {
			return fmt.Errorf("the host %s was stopped (start it: devboxen host start %s)", h.Name, h.Name)
		}
		say("starting the host %s; while it runs, it costs money", h.Name)
		if _, err := awsDo(h, "", "ec2", "start-instances", "--instance-ids", h.AWS.Instance); err != nil {
			return err
		}
	case "pending":
	default:
		return fmt.Errorf("the host %s is %s", h.Name, inst.State)
	}
	if _, err := awsDo(h, "", "ec2", "wait", "instance-running", "--instance-ids", h.AWS.Instance); err != nil {
		return err
	}
	say("waiting for SSH on %s", h.Name)
	for i := 0; i < 60; i++ {
		if reachable(h, 5) {
			say("the host %s is up", h.Name)
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("the host %s runs, but SSH does not answer", h.Name)
}

// onDemandHost: the named host, or the only on-demand one.
func (a *app) onDemandHost(args []string) (*config.Host, error) {
	if len(args) > 0 {
		h, err := a.cfg.Host(strings.TrimPrefix(args[0], "@"))
		if err != nil {
			return nil, err
		}
		if !h.OnDemand() {
			return nil, fmt.Errorf("the host %s is not an on-demand host: it is not started or stopped from here", h.Name)
		}
		return h, nil
	}
	var found []*config.Host
	for _, name := range a.cfg.Order {
		if h := a.cfg.Hosts[name]; h.OnDemand() {
			found = append(found, h)
		}
	}
	switch len(found) {
	case 0:
		return nil, errors.New("no on-demand host in devboxen.toml (a host with an [hosts.<name>.aws] section)")
	case 1:
		return found[0], nil
	}
	return nil, errors.New("which host? there are several on-demand hosts")
}

func (a *app) cmdHost(args []string) error {
	if len(args) == 0 {
		return errors.New("devboxen host status|start|stop|setup|add [host]")
	}
	action, args := args[0], args[1:]
	if action == "add" {
		return a.cmdHostAdd(args)
	}
	if action == "setup" {
		h, err := a.hostArg(args)
		if err != nil {
			return err
		}
		return a.hostSetup(h)
	}
	h, err := a.onDemandHost(args)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		inst, err := describe(h, true)
		if err != nil {
			return err
		}
		ip := inst.IP
		if ip == "" {
			ip = "no address while stopped"
		}
		fmt.Printf("%s: %s (%s, %s)\n", h.Name, inst.State, inst.Type, ip)
		return nil
	case "start":
		return a.ensureUp(h, true)
	case "stop":
		inst, err := describe(h, true)
		if err != nil {
			return err
		}
		if inst.State != "running" {
			say("the host %s is %s", h.Name, inst.State)
			return nil
		}
		if out, err := a.adminOutput(h, 10, "list"); err == nil {
			fmt.Print(out)
		}
		fmt.Printf("\nStopping %s ends every session in its boxes, Claude included. Their data stays.\n", h.Name)
		if !confirm(fmt.Sprintf("Stop the host %s?", h.Name)) {
			return errors.New("not stopped")
		}
		if _, err := awsDo(h, "", "ec2", "stop-instances", "--instance-ids", h.AWS.Instance); err != nil {
			return err
		}
		say("the host %s is stopping; while stopped, only its disks and address cost money", h.Name)
		return nil
	}
	return fmt.Errorf("unknown: devboxen host %s (status, start, stop, setup, add)", action)
}
