# An AWS host

Besides servers that are always on, devboxen can use an AWS EC2 instance that runs only
while you need it: a bigger machine for a few hours, without paying for it all month. The
CLI starts it when you connect to one of its boxes, and it stops every night in any case.
While it is stopped, only its disks, their snapshots and its fixed address cost money.

## Adding one

You need, on your computer, the [AWS CLI](https://aws.amazon.com/cli/) with a profile for
the account the host goes into (best an account of its own; `aws configure sso` sets up a
profile), and [OpenTofu](https://opentofu.org/docs/intro/install/). Then, in your
devboxen directory:

```bash
devboxen host add            # choose "a new AWS EC2 instance"
```

It asks for the profile, the region, the instance type (default `m8a.2xlarge`: 8 CPUs,
32 GB), the availability zone, the size of the data volume, the time zone of the nightly
stop and the SSH keys that may log in. Then it writes an OpenTofu configuration to
`aws/<name>/`, which uses devboxen's module [`tofu/aws-host`](../tofu/aws-host/), and runs
it: OpenTofu shows what it will create and asks before it does. The result goes into
`devboxen.toml`, and devboxen sets the new host up.

What it creates: the instance with Ubuntu 24.04, a separate data volume for everything the
boxes keep (with daily snapshots, kept 7 days), a fixed public address, a firewall that
lets in SSH only, and a schedule that stops the instance every night.

## Afterwards

```bash
devboxen shell @cloud        # starts the host if it is stopped (takes about a minute)
devboxen host stop cloud     # stops it (ends its sessions; the data stays)
devboxen host status cloud
devboxen costs cloud         # what it has cost (today, yesterday, 7 days, this month)
```

To change something, for example a bigger instance type, edit `aws/<name>/main.tf` and run
`tofu apply` there (a new instance type takes effect at the next start). Commit the whole
directory, including `terraform.tfstate`: it holds no secrets, and without it OpenTofu no
longer knows the resources. The [module's variables](../tofu/aws-host/variables.tf)
list everything you can set.

## Your own configuration

If you manage AWS with OpenTofu or Terraform already, use the module from there:

```hcl
module "devbox_host" {
  source = "git::https://github.com/dx-tooling/devboxen.git//tofu/aws-host?ref=<version>"

  availability_zone    = "eu-central-1a"
  root_ssh_public_keys = ["ssh-ed25519 AAAA… you@laptop"]
}
```

Then add the host to `devboxen.toml` by hand with the module's outputs
([config.md](config.md#hostsname)), and run `devboxen deploy`.
