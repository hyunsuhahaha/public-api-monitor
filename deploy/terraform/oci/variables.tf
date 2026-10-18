variable "tenancy_ocid" { type = string }
variable "user_ocid" { type = string }
variable "fingerprint" { type = string }
variable "private_key_path" { type = string }

variable "region" {
  description = "Home region (Always Free A1 capacity is only in the home region)"
  type        = string
  default     = "ap-chuncheon-1"
}

variable "compartment_ocid" {
  description = "Compartment for everything (the tenancy OCID uses the root compartment)"
  type        = string
}

variable "ssh_public_key_path" {
  type    = string
  default = "~/.ssh/id_ed25519.pub"
}

variable "admin_cidr" {
  description = "Where SSH is allowed from (your IP /32)"
  type        = string
}

variable "name" {
  type    = string
  default = "pamon"
}

# Always Free: up to 4 OCPU / 24 GB of A1 in total (2 / 12 per the 2026 docs; keep under it)
variable "ocpus" {
  type    = number
  default = 2
}

variable "memory_gb" {
  type    = number
  default = 12
}

variable "boot_volume_gb" {
  type    = number
  default = 50
}

variable "location_name" {
  description = "pamon -location of the central (shown on the status page)"
  type        = string
  default     = "oci-chuncheon"
}
