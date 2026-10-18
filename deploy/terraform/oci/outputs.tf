output "central_public_ip" {
  value = oci_core_instance.central.public_ip
}

output "ssh" {
  value = "ssh ubuntu@${oci_core_instance.central.public_ip}"
}
