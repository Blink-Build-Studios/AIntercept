# AWS provisioning for aintercept: an S3 bucket for telemetry plus an IRSA IAM
# role the observer ServiceAccount assumes for KEYLESS writes (no static creds
# in the cluster). Assumes an existing EKS cluster with an OIDC provider.
#
# Usage:
#   terraform apply \
#     -var cluster_oidc_provider_arn=arn:aws:iam::<acct>:oidc-provider/oidc.eks.<region>.amazonaws.com/id/<id> \
#     -var cluster_oidc_provider_url=oidc.eks.<region>.amazonaws.com/id/<id> \
#     -var bucket_name=friendly-aintercept-telemetry
#
# Output `observer_role_arn` → pass as observer.serviceAccount.roleArn to Helm.

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# ── Telemetry bucket ─────────────────────────────────────────────────────────
resource "aws_s3_bucket" "telemetry" {
  bucket = var.bucket_name
}

resource "aws_s3_bucket_lifecycle_configuration" "telemetry" {
  bucket = aws_s3_bucket.telemetry.id
  rule {
    id     = "expire-raw-telemetry"
    status = "Enabled"
    expiration {
      days = var.retention_days
    }
  }
}

resource "aws_s3_bucket_public_access_block" "telemetry" {
  bucket                  = aws_s3_bucket.telemetry.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# ── IRSA: IAM role the observer ServiceAccount assumes ───────────────────────
data "aws_iam_policy_document" "assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [var.cluster_oidc_provider_arn]
    }
    # Scope the trust to exactly the observer ServiceAccount in its namespace.
    condition {
      test     = "StringEquals"
      variable = "${var.cluster_oidc_provider_url}:sub"
      values   = ["system:serviceaccount:${var.namespace}:${var.service_account_name}"]
    }
    condition {
      test     = "StringEquals"
      variable = "${var.cluster_oidc_provider_url}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "observer" {
  name               = "${var.bucket_name}-observer"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

# Least privilege: write-only to this bucket/prefix.
data "aws_iam_policy_document" "put" {
  statement {
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.telemetry.arn}/${var.prefix}/*"]
  }
}

resource "aws_iam_role_policy" "observer_put" {
  name   = "s3-put-telemetry"
  role   = aws_iam_role.observer.id
  policy = data.aws_iam_policy_document.put.json
}
