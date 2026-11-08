output "public_ip" {
  description = "The Elastic IP: the host's address in devboxen.toml (ssh) and the inventory."
  value       = aws_eip.host.public_ip
}

output "instance_id" {
  description = "For devboxen host start/stop (devboxen.toml: aws.instance)."
  value       = aws_instance.host.id
}

output "data_volume_id" {
  description = "devboxen_data_volume_id: Ansible finds the data volume by it (/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol…)."
  value       = aws_ebs_volume.data.id
}

output "ssh" {
  value = "ssh root@${aws_eip.host.public_ip}"
}
