variable "region" {
  description = "AWS region for the S3 bucket."
  type        = string
}

variable "bucket_name" {
  description = "S3 bucket name for telemetry. Globally unique."
  type        = string
}

variable "prefix" {
  description = "Object key prefix the observer writes under (scopes the IAM policy)."
  type        = string
  default     = "aintercept"
}

variable "retention_days" {
  description = "Days to retain telemetry objects before lifecycle expiry."
  type        = number
  default     = 30
}

variable "cluster_oidc_provider_arn" {
  description = "ARN of the EKS cluster's IAM OIDC provider."
  type        = string
}

variable "cluster_oidc_provider_url" {
  description = "URL of the EKS OIDC provider (no https:// prefix), e.g. oidc.eks.us-east-1.amazonaws.com/id/ABC123."
  type        = string
}

variable "namespace" {
  description = "Kubernetes namespace the observer runs in."
  type        = string
  default     = "default"
}

variable "service_account_name" {
  description = "Observer ServiceAccount name (must match observer.serviceAccount.name in Helm values)."
  type        = string
  default     = "aintercept-observer"
}
