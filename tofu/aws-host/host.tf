# The devbox host: one EC2 instance that is stopped when not in use. While stopped, only
# its volumes and the Elastic IP cost money; both stay, so it comes back as it was (minus
# the running tmux/Claude sessions, which end with every stop).
#
# Layout:
# - root volume: Ubuntu 24.04 only; reproducible with ansible/ (site.yml)
# - data volume: /var/lib/docker (the boxes and their volumes) and /var/lib/devbox (box
#   settings, the shared Claude token); mounted by Ansible (ansible/data-volume.yml).
#   Snapshotted daily, protected from deletion; survives replacing the instance.
#
# cloud-init does only what Ansible cannot: no default `ubuntu` user (host UID 1000 stays
# free, see docs/box.md) and root reachable with var.root_ssh_public_keys. Everything else
# is Ansible.

# Canonical's current Ubuntu 24.04 AMI at creation time. Later AMIs do not replace the
# instance (ignore_changes below); the OS is kept current by unattended-upgrades.
data "aws_ssm_parameter" "ubuntu_ami" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

locals {
  cloud_init = <<-EOT
    #cloud-config
    # ${local.repo_note}
    hostname: ${var.hostname}
    users: []
    disable_root: false
    ssh_pwauth: false
    ssh_authorized_keys:
    ${indent(2, join("", [for k in var.root_ssh_public_keys : "- ${trimspace(k)}\n"]))}
  EOT
}

resource "aws_instance" "host" {
  ami                    = nonsensitive(data.aws_ssm_parameter.ubuntu_ami.value)
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.host.id]
  iam_instance_profile   = aws_iam_instance_profile.host.name
  user_data              = local.cloud_init

  # A shutdown from inside (e.g. an idle timer's poweroff) stops, never terminates.
  instance_initiated_shutdown_behavior = "stop"
  disable_api_termination              = true

  # IMDSv2 only, and hop limit 1: the instance role's credentials are out of reach for the
  # boxes and their containers (each is at least one network hop away from the host).
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.root_volume_gb
    encrypted             = true
    delete_on_termination = true
    tags                  = { Name = "${local.name}-root" }
  }

  tags = { Name = local.name }

  lifecycle {
    # A newer AMI or edited cloud-init (first boot only) must not replace or restart it.
    ignore_changes = [ami, user_data]
  }
}

resource "aws_eip" "host" {
  domain = "vpc"
  tags   = { Name = local.name }

  lifecycle {
    prevent_destroy = true # the address the CLI and Ansible know
  }
}

resource "aws_eip_association" "host" {
  instance_id   = aws_instance.host.id
  allocation_id = aws_eip.host.id
}

resource "aws_ebs_volume" "data" {
  availability_zone = var.availability_zone
  type              = "gp3"
  size              = var.data_volume_gb
  iops              = var.data_volume_iops
  throughput        = var.data_volume_throughput
  encrypted         = true
  final_snapshot    = true # if it is ever deleted after all

  tags = {
    Name     = "${local.name}-data"
    Snapshot = "daily" # picked up by the DLM policy below
  }

  lifecycle {
    prevent_destroy = true # the boxes' data
  }
}

resource "aws_volume_attachment" "data" {
  device_name                    = "/dev/sdf" # appears as an NVMe device; Ansible finds it by volume ID
  volume_id                      = aws_ebs_volume.data.id
  instance_id                    = aws_instance.host.id
  stop_instance_before_detaching = true
}

# --- Instance role: SSM Session Manager only --------------------------------------------------

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "host" {
  name               = "${local.name}-host"
  description        = "devbox host: SSM Session Manager only - ${local.repo_note}"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "host_ssm" {
  role       = aws_iam_role.host.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "host" {
  name = "${local.name}-host"
  role = aws_iam_role.host.name
}

# --- Daily snapshots of the data volume ----------------------------------------------------------

data "aws_iam_policy_document" "dlm_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dlm" {
  name               = "${local.name}-dlm"
  description        = "Data Lifecycle Manager: daily data-volume snapshots - ${local.repo_note}"
  assume_role_policy = data.aws_iam_policy_document.dlm_assume.json
}

resource "aws_iam_role_policy_attachment" "dlm" {
  role       = aws_iam_role.dlm.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "data" {
  # Only [0-9A-Za-z _-] allowed here, so no URL; the policy's tags carry the repo.
  description        = coalesce(var.snapshot_policy_description, "${local.name} data volume daily")
  execution_role_arn = aws_iam_role.dlm.arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Snapshot = "daily" }

    schedule {
      name      = "daily"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["03:00"] # UTC
      }

      retain_rule {
        count = var.snapshot_retain_days
      }

      tags_to_add = { SnapshotOf = "${local.name}-data" }
    }
  }
}
