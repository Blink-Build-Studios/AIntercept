variable "region" {
  description = "DigitalOcean region (must support Spaces: nyc3, ams3, sgp1, sfo3, fra1, syd1, blr1)."
  type        = string
  default     = "nyc3"
}

variable "cluster_name" {
  description = "Name of the DOKS cluster."
  type        = string
  default     = "aintercept-demo"
}

variable "k8s_version" {
  description = "DOKS Kubernetes version slug (see: doctl kubernetes options versions)."
  type        = string
  default     = "1.37.0-do.1"
}

variable "node_size" {
  description = "Droplet size slug for the node pool."
  type        = string
  default     = "s-2vcpu-4gb"
}

variable "node_count" {
  description = "Initial / minimum node count."
  type        = number
  default     = 2
}

variable "node_max" {
  description = "Maximum node count for cluster autoscaling."
  type        = number
  default     = 6
}

variable "bucket_name" {
  description = "Spaces bucket (Space) name for telemetry. Globally unique."
  type        = string
}

variable "retention_days" {
  description = "Days to retain telemetry objects before lifecycle expiry."
  type        = number
  default     = 30
}

variable "spaces_access_id" {
  description = "Spaces access key id with bucket-create rights (separate from the API token)."
  type        = string
  sensitive   = true
}

variable "spaces_secret_key" {
  description = "Spaces secret key."
  type        = string
  sensitive   = true
}
