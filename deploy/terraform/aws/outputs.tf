output "bucket_name" {
  description = "Telemetry bucket — pass as objectStore.bucket."
  value       = aws_s3_bucket.telemetry.id
}

output "observer_role_arn" {
  description = "IRSA role ARN — pass as observer.serviceAccount.roleArn (cloud=aws)."
  value       = aws_iam_role.observer.arn
}

output "region" {
  value = var.region
}
