package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dx-tooling/devboxen/internal/config"
)

// devboxen host add: another host, asked step by step. A server you have is checked and
// added to devboxen.toml. A new AWS host is created with OpenTofu and devboxen's module
// (tofu/aws-host), from a root module written to aws/<name>/ next to devboxen.toml; its
// outputs go into devboxen.toml. Then the hosts are set up (devboxen deploy).

func (a *app) cmdHostAdd(args []string) error {
	pos, _, err := splitFlags(args, map[string]bool{})
	if err != nil {
		return err
	}
	name := first(pos)
	taken := map[string]bool{}
	for n := range a.cfg.Hosts {
		taken[n] = true
	}
	if name != "" && (taken[name] || !config.NameRE.MatchString(name)) {
		return fmt.Errorf("%q: not a free host name (1-11 characters: a-z, 0-9 and -)", name)
	}
	fmt.Println(`What kind of host?
  1  a server you have: Ubuntu 24.04, you log in as root with your SSH key
  2  a new AWS EC2 instance that runs only while you use it (created in your AWS account)`)
	for {
		switch ask("Which?", "1") {
		case "1":
			if name == "" {
				name, _ = freeName("vps", taken)
			}
			n, h, _ := askServer(name, taken)
			if err := a.appendHost(n, h); err != nil {
				return err
			}
			return a.afterAdd(n)
		case "2":
			if name == "" {
				name, _ = freeName("cloud", taken)
			}
			return a.addAWS(name, taken)
		}
	}
}

// freeName: base, or base2, base3… if taken.
func freeName(base string, taken map[string]bool) (string, bool) {
	if !taken[base] {
		return base, true
	}
	for i := 2; i < 100; i++ {
		if n := base + strconv.Itoa(i); !taken[n] {
			return n, true
		}
	}
	return "", false
}

// appendHost adds a [hosts.<name>] section to devboxen.toml (checked by reading it back).
func (a *app) appendHost(name string, h config.Host) error {
	path := a.cfg.Path
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(old)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n" + config.RenderHost(name, h)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		_ = os.WriteFile(path, old, 0o644)
		return fmt.Errorf("could not add the host (devboxen.toml left as it was): %w", err)
	}
	a.cfg = cfg
	say("added the host %s to %s", name, shortPath(path))
	return nil
}

// afterAdd sets up the new host. The other hosts that run are updated too: their firewalls
// learn the new address, the front gets a tunnel to it.
func (a *app) afterAdd(name string) error {
	h, err := a.cfg.Host(name)
	if err != nil {
		return err
	}
	fmt.Printf("\nNow devboxen sets up %s (about 10 minutes) and updates the other hosts, so that they\nknow it (firewall, the browser interface). Boxes keep running.\n", name)
	if !confirm("Set it up now?") {
		fmt.Printf("  Later: devboxen deploy, then devboxen host setup %s\n", name)
		return nil
	}
	hosts := []string{}
	for _, n := range a.cfg.Order {
		o := a.cfg.Hosts[n]
		if n == name || reachable(o, 8) {
			hosts = append(hosts, n)
		} else {
			say("%s is not reachable (stopped?): update it once it runs: devboxen deploy %s", n, n)
		}
	}
	if err := a.cmdDeploy(hosts); err != nil {
		return err
	}
	return a.hostSetup(h)
}

// --- AWS ------------------------------------------------------------------------------------

func tofuBin() (string, error) {
	for _, b := range []string{"tofu", "terraform"} {
		if _, err := exec.LookPath(b); err == nil {
			return b, nil
		}
	}
	return "", errors.New("creating an AWS host needs OpenTofu on this computer (https://opentofu.org/docs/intro/install/, e.g. brew install opentofu)")
}

// awsHostSettings are the answers, written into the root module.
type awsHostSettings struct {
	Name, Profile, Region, SSOSession, Account string
	InstanceType, AZ, TimeZone, ModuleSource   string
	DataGB                                     int
}

func (a *app) addAWS(name string, taken map[string]bool) error {
	tofu, err := tofuBin()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return errors.New("creating an AWS host needs the AWS CLI on this computer (https://aws.amazon.com/cli/)")
	}

	for {
		name = ask("A short name for it (a-z, 0-9, -):", name)
		if config.NameRE.MatchString(name) && !taken[name] {
			break
		}
		fmt.Println("  1-11 characters (a-z, 0-9 and -), not taken")
	}
	dir := filepath.Join(a.cfg.Dir, "aws", name)
	if fileExists(filepath.Join(dir, "main.tf")) {
		say("%s is there already: applying it as it is", shortPath(dir))
		return a.applyAWS(tofu, dir, name)
	}

	fmt.Println(`
devboxen creates the host with OpenTofu in an AWS account of yours: an EC2 instance, a
data volume with daily snapshots, a fixed address, and a nightly stop as a safety net.
It needs an AWS CLI profile with the rights to create these (best: an account of its own).`)
	s := awsHostSettings{Name: name}
	if s.Profile, err = askProfile(); err != nil {
		return err
	}
	s.Region = ask("Region:", awsConfigGet(s.Profile, "region"))
	s.SSOSession = awsConfigGet(s.Profile, "sso_session")
	probe := &config.Host{Name: name, AWS: &config.AWS{Profile: s.Profile, Region: s.Region, SSOSession: s.SSOSession}}
	if s.Account, err = awsDo(probe, "", "sts", "get-caller-identity", "--query", "Account", "--output", "text"); err != nil {
		return err
	}
	say("AWS account %s, region %s", s.Account, s.Region)

	var zones []string
	for {
		s.InstanceType = ask("Instance type:", "m8a.2xlarge")
		out, err := awsDo(probe, "", "ec2", "describe-instance-type-offerings", "--location-type", "availability-zone",
			"--filters", "Name=instance-type,Values="+s.InstanceType, "--query", "InstanceTypeOfferings[].Location", "--output", "text")
		if err != nil {
			return err
		}
		zones = strings.Fields(out)
		if len(zones) > 0 {
			break
		}
		fmt.Printf("  %s is not offered in %s\n", s.InstanceType, s.Region)
	}
	sort.Strings(zones)
	if out, err := awsRun(probe, "", "ec2", "describe-instance-types", "--instance-types", s.InstanceType,
		"--query", "InstanceTypes[0].[VCpuInfo.DefaultVCpus,MemoryInfo.SizeInMiB]", "--output", "text"); err == nil {
		if f := strings.Fields(out); len(f) == 2 {
			mib, _ := strconv.Atoi(f[1])
			line := fmt.Sprintf("  %s CPUs, %d GB memory", f[0], mib/1024)
			if p, err := hourlyPrice(probe, s.InstanceType); err == nil {
				line += fmt.Sprintf(", $%.2f per hour while it runs", p)
			}
			fmt.Println(line)
		}
	}
	for {
		s.AZ = ask("Availability zone ("+strings.Join(zones, ", ")+"):", zones[0])
		if contains(zones, s.AZ) {
			break
		}
	}
	for {
		s.DataGB, err = strconv.Atoi(ask("Size of the data volume in GB (the boxes' homes and Docker data; can grow later):", "150"))
		if err == nil && s.DataGB >= 20 {
			break
		}
		fmt.Println("  a number, at least 20")
	}
	fmt.Println("It is stopped every night at midnight, in case you forget it (start it again any time).")
	s.TimeZone = ask("In which time zone?", localZone())

	keys := sshPublicKeys()
	if len(keys) == 0 {
		return errors.New("no SSH public key found (ssh-add -L, ~/.ssh/*.pub): devboxen logs in to the host with yours")
	}
	fmt.Println("SSH keys that may log in as root:")
	for i, k := range keys {
		fmt.Printf("  %d  %s\n", i+1, keyLabel(k))
	}
	var chosen []string
	for {
		chosen = pickKeys(keys, ask("Which (e.g. 1 or 1,2)?", "1"))
		if len(chosen) > 0 {
			break
		}
	}

	src, err := moduleSource()
	if err != nil {
		return err
	}
	s.ModuleSource = src
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "root_authorized_keys"), []byte(strings.Join(chosen, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(renderAWSRoot(s)), 0o644); err != nil {
		return err
	}
	if err := ensureGitignore(dir, ".terraform/"); err != nil {
		return err
	}
	say("wrote %s (change it any time, then tofu apply there)", shortPath(dir))
	fmt.Printf(`
This creates, in the AWS account %s (%s):
  an %s in %s, a %d GB data volume with daily snapshots (kept 7 days),
  a fixed public address, and a stop every night at 00:00 (%s).
While it runs, the instance costs per hour; while it is stopped, only the volumes, the
snapshots and the address cost money.
`, s.Account, s.Region, s.InstanceType, s.AZ, s.DataGB, s.TimeZone)
	if !confirm("Create it?") {
		fmt.Printf("  Later: devboxen host add %s (uses what is written in %s)\n", name, shortPath(dir))
		return nil
	}
	return a.applyAWS(tofu, dir, name)
}

// applyAWS creates (or updates) the host from its root module, adds it to devboxen.toml and
// sets it up.
func (a *app) applyAWS(tofu, dir, name string) error {
	run := func(args ...string) error {
		cmd := exec.Command(tofu, append([]string{"-chdir=" + dir}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	say("%s init", tofu)
	if err := run("init", "-input=false"); err != nil {
		return fmt.Errorf("%s init: %w", tofu, err)
	}
	fmt.Printf("\nOpenTofu now shows what it creates and asks before it does (answer yes).\n\n")
	if err := run("apply"); err != nil {
		return fmt.Errorf("%s apply: %w (run devboxen host add %s again to continue)", tofu, err, name)
	}
	cmd := exec.Command(tofu, "-chdir="+dir, "output", "-json", "devboxen")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s output: %w", tofu, err)
	}
	h, err := parseAWSOutput(out)
	if err != nil {
		return err
	}
	if old := a.cfg.Hosts[name]; old != nil {
		if old.SSH != h.SSH || old.AWS == nil || old.AWS.Instance != h.AWS.Instance {
			return fmt.Errorf("devboxen.toml has a host %s already, with other values than %s: fix one of them", name, dir)
		}
	} else if err := a.appendHost(name, h); err != nil {
		return err
	}
	nh, _ := a.cfg.Host(name)
	// The first login stores the host key (Ansible and the tunnel need it known). A stopped
	// host (run again after a stop) is started.
	if inst, err := describe(nh, true); err == nil && inst.State == "stopped" {
		say("starting %s", name)
		if _, err := awsDo(nh, "", "ec2", "start-instances", "--instance-ids", nh.AWS.Instance); err != nil {
			return err
		}
	}
	say("waiting for %s to boot", name)
	for i := 0; ; i++ {
		if _, err := checkServer(nh); err == nil {
			break
		} else if i == 40 {
			return fmt.Errorf("cannot log in to %s: %v", nh.SSH, err)
		}
		time.Sleep(5 * time.Second)
	}
	return a.afterAdd(name)
}

func renderAWSRoot(s awsHostSettings) string {
	q := config.Quote
	return fmt.Sprintf(`# The host %[1]s: an on-demand AWS EC2 instance for devboxen, made with devboxen's module
# (tofu/aws-host). Written by devboxen host add; change a value, then run tofu apply here.
#
# The state (terraform.tfstate) is kept in this directory: commit it with the rest (it
# holds no secrets). Without it, OpenTofu no longer knows these resources. Or move it to a
# backend of your own (https://opentofu.org/docs/language/settings/backends/configuration/).

locals {
  profile     = %[2]s
  region      = %[3]s
  sso_session = %[4]s # for logging in again when the login has expired; "" if none
}

terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  profile = local.profile
  region  = local.region

  # Never act on another account, whatever credentials are around.
  allowed_account_ids = [%[5]s]

  default_tags {
    tags = {
      managed-by    = "devboxen"
      devboxen-host = %[1]s
    }
  }
}

module "host" {
  source = %[6]s

  name                 = %[7]s
  hostname             = %[7]s
  instance_type        = %[8]s
  availability_zone    = %[9]s
  data_volume_gb       = %[10]d
  auto_stop_timezone   = %[11]s
  root_ssh_public_keys = compact(split("\n", file("${path.module}/root_authorized_keys")))
}

# Read by devboxen host add: the host's entry in devboxen.toml.
output "devboxen" {
  value = {
    ssh         = "root@${module.host.public_ip}"
    profile     = local.profile
    region      = local.region
    sso_session = local.sso_session
    instance    = module.host.instance_id
    data_volume = module.host.data_volume_id
  }
}
`, q(s.Name), q(s.Profile), q(s.Region), q(s.SSOSession), q(s.Account), q(s.ModuleSource),
		q("devboxen-"+s.Name), q(s.InstanceType), q(s.AZ), s.DataGB, q(s.TimeZone))
}

func parseAWSOutput(out []byte) (config.Host, error) {
	var o struct {
		SSH        string `json:"ssh"`
		Profile    string `json:"profile"`
		Region     string `json:"region"`
		SSOSession string `json:"sso_session"`
		Instance   string `json:"instance"`
		DataVolume string `json:"data_volume"`
	}
	if err := json.Unmarshal(out, &o); err != nil || o.SSH == "" || o.Instance == "" {
		return config.Host{}, fmt.Errorf("unexpected output of the root module: %s", strings.TrimSpace(string(out)))
	}
	return config.Host{SSH: o.SSH, AWS: &config.AWS{Profile: o.Profile, Region: o.Region,
		Instance: o.Instance, SSOSession: o.SSOSession, DataVolume: o.DataVolume}}, nil
}

// moduleSource: where tofu gets devboxen's module: the release this CLI is (its tag), the
// commit of a checkout build, else main. $DEVBOXEN_TOFU_MODULE overrides it (e.g. a local
// path while working on the module).
func moduleSource() (string, error) {
	if s := os.Getenv("DEVBOXEN_TOFU_MODULE"); s != "" {
		return s, nil
	}
	return "git::https://github.com/dx-tooling/devboxen.git//tofu/aws-host?ref=" + moduleRef(version, buildVersion()), nil
}

var (
	shaRE    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	pseudoRE = regexp.MustCompile(`-([0-9a-f]{12})(\+dirty)?$`)
	tagRE    = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)
)

// moduleRef: v (set at build time), else the module version go install recorded.
func moduleRef(v, build string) string {
	if strings.HasPrefix(v, "checkout-") && shaRE.MatchString(strings.TrimPrefix(v, "checkout-")) {
		return strings.TrimPrefix(v, "checkout-")
	}
	for _, s := range []string{v, build} {
		if m := pseudoRE.FindStringSubmatch(s); m != nil {
			return m[1]
		}
		if tagRE.MatchString(s) {
			return s
		}
	}
	return "main"
}

func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Version
	}
	return ""
}

// askProfile lets you pick one of the AWS CLI's profiles.
func askProfile() (string, error) {
	out, err := exec.Command("aws", "configure", "list-profiles").Output()
	profiles := strings.Fields(string(out))
	if err != nil || len(profiles) == 0 {
		return "", errors.New("no AWS CLI profile: make one first (aws configure sso, or aws configure), then run this again")
	}
	fmt.Println("AWS CLI profiles:")
	for i, p := range profiles {
		fmt.Printf("  %d  %s\n", i+1, p)
	}
	for {
		v := ask("Which?", "")
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(profiles) {
			return profiles[n-1], nil
		}
		if contains(profiles, v) {
			return v, nil
		}
	}
}

func awsConfigGet(profile, key string) string {
	out, _ := exec.Command("aws", "configure", "get", key, "--profile", profile).Output()
	return strings.TrimSpace(string(out))
}

// localZone: this computer's time zone (an IANA name), else UTC.
func localZone() string {
	if tz := os.Getenv("TZ"); strings.Contains(tz, "/") {
		return strings.TrimPrefix(tz, ":")
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// sshPublicKeys: the keys in the SSH agent and in ~/.ssh/*.pub, each once.
func sshPublicKeys() []string {
	var lines []string
	if out, err := exec.Command("ssh-add", "-L").Output(); err == nil {
		lines = append(lines, strings.Split(string(out), "\n")...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		files, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				lines = append(lines, strings.Split(string(b), "\n")...)
			}
		}
	}
	return uniqueKeys(lines)
}

func uniqueKeys(lines []string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || !(strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-") || strings.HasPrefix(f[0], "sk-")) {
			continue
		}
		if !seen[f[1]] {
			seen[f[1]] = true
			keys = append(keys, strings.TrimSpace(l))
		}
	}
	return keys
}

// keyLabel: a key's type, comment and the end of its data.
func keyLabel(k string) string {
	f := strings.Fields(k)
	tail := f[1]
	if len(tail) > 12 {
		tail = "…" + tail[len(tail)-12:]
	}
	return strings.Join(append([]string{f[0], tail}, f[2:]...), " ")
}

// pickKeys: the keys a list like "1,3" names (nil if any number is wrong).
func pickKeys(keys []string, answer string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > len(keys) {
			return nil
		}
		if !contains(out, keys[n-1]) {
			out = append(out, keys[n-1])
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
