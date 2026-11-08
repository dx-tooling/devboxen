# A VPC of its own with one public subnet: the host gets an Elastic IP and talks to the
# internet directly. No NAT gateway (it would cost more than the host while stopped), no
# private subnets (nothing else lives here).

locals {
  name      = var.name
  repo_note = var.note
}

resource "aws_vpc" "this" {
  cidr_block           = "10.80.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.this.id
  cidr_block        = "10.80.0.0/24"
  availability_zone = var.availability_zone
  tags              = { Name = "${local.name}-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }

  tags = { Name = "${local.name}-public" }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

# The VPC's default security group: no rules at all, so nothing can use it by accident.
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${local.name}-default-unused" }
}

resource "aws_security_group" "host" {
  name        = "${local.name}-host"
  description = "devbox host: SSH in (key-only), everything out - ${local.repo_note}"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${local.name}-host" }
}

# SSH from anywhere: sshd is key-only (ansible/host.yml), and a laptop's address changes.
# SSM Session Manager is the way in if SSH is ever broken.
resource "aws_vpc_security_group_ingress_rule" "ssh" {
  security_group_id = aws_security_group.host.id
  description       = "SSH (key-only)"
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.host.id
  description       = "everything out (packages, images, GitHub, Claude)"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}
