variable "name" {
  description = "Prefix of every resource's name (and Name tag)."
  type        = string
  default     = "devboxen"
}

variable "note" {
  description = "Appended to resource descriptions, for whoever finds them in the console. Changing it replaces the security group."
  type        = string
  default     = "managed with devboxen (tofu/aws-host)"
}

variable "hostname" {
  description = "The instance's hostname (cloud-init, first boot only)."
  type        = string
  default     = "devboxen-aws"
}

variable "root_ssh_public_keys" {
  description = "SSH public keys that may log in as root (the devboxen CLI and Ansible connect as root). First boot only; later changes go through the host itself."
  type        = list(string)
}

variable "instance_type" {
  description = "Changeable at any time: stop the instance, apply, start it (IP and volumes stay). Per-core speed is the same for every size of a family."
  type        = string
  default     = "m8a.2xlarge" # 8 vCPU (each a full core), 32 GiB
}

variable "availability_zone" {
  description = "EBS volumes live in one AZ; instance and data volume must share it (e.g. eu-central-1a)."
  type        = string
}

variable "root_volume_gb" {
  type    = number
  default = 30 # the OS only; everything that matters is on the data volume
}

# The data volume can be grown and retuned while in use (aws ec2 modify-volume, or here);
# growing it then needs `growpart`/`resize2fs` on the host. It cannot shrink.
variable "data_volume_gb" {
  type    = number
  default = 150
}

variable "data_volume_iops" {
  type    = number
  default = 4000 # gp3 includes 3000; the instance's EBS baseline is 12000 (m8a.2xlarge)
}

variable "data_volume_throughput" {
  type    = number
  default = 250 # MB/s; gp3 includes 125
}

variable "snapshot_retain_days" {
  type    = number
  default = 7
}

variable "snapshot_policy_description" {
  description = "The DLM policy's description (only [0-9A-Za-z _-]); default: \"<name> data volume daily\"."
  type        = string
  default     = null
}
