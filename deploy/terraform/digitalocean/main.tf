# DigitalOcean provisioning for aintercept: a DOKS cluster, a Spaces bucket for
# telemetry, and a scoped Spaces access key for the observer.
#
# Spaces has no IRSA equivalent, so the observer authenticates with a static
# access key (output below) wired into the Helm release via a Secret.
#
# Usage:
#   export DIGITALOCEAN_TOKEN=dop_v1_...
#   export SPACES_ACCESS_KEY_ID=...        # a Spaces key with bucket-create rights
#   export SPACES_SECRET_ACCESS_KEY=...    # (Spaces keys are separate from the API token)
#   terraform init && terraform apply

terraform {
  required_version = ">= 1.5"
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.43"
    }
  }
}

provider "digitalocean" {
  # Reads DIGITALOCEAN_TOKEN from the environment.
  # Spaces bucket management uses the spaces keys below.
  spaces_access_id  = var.spaces_access_id
  spaces_secret_key = var.spaces_secret_key
}

# ── Telemetry bucket (DigitalOcean Space) ────────────────────────────────────
resource "digitalocean_spaces_bucket" "telemetry" {
  name   = var.bucket_name
  region = var.region
  acl    = "private"

  lifecycle_rule {
    id      = "expire-raw-telemetry"
    enabled = true
    expiration {
      days = var.retention_days
    }
  }
}

# ── DOKS cluster ─────────────────────────────────────────────────────────────
resource "digitalocean_kubernetes_cluster" "aintercept" {
  name    = var.cluster_name
  region  = var.region
  version = var.k8s_version

  node_pool {
    name       = "default"
    size       = var.node_size
    node_count = var.node_count
    auto_scale = true
    min_nodes  = var.node_count
    max_nodes  = var.node_max
  }
}

# NOTE: Spaces access keys scoped to a single bucket are created via the DO API
# (doctl spaces keys create) or the cloud console; the Terraform provider does
# not yet manage per-bucket Spaces keys. The DEPLOY_DOKS.md quickstart mints one
# with doctl and passes it to Helm. This module provisions the bucket + cluster;
# the scoped key is a one-line doctl step documented in the quickstart.
