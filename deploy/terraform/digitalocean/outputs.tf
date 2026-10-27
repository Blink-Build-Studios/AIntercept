output "cluster_id" {
  description = "DOKS cluster ID (use: doctl kubernetes cluster kubeconfig save <id>)."
  value       = digitalocean_kubernetes_cluster.aintercept.id
}

output "cluster_name" {
  value = digitalocean_kubernetes_cluster.aintercept.name
}

output "bucket_name" {
  description = "Telemetry Space name — pass as objectStore.bucket."
  value       = digitalocean_spaces_bucket.telemetry.name
}

output "bucket_endpoint" {
  description = "S3-compatible endpoint — pass as objectStore.endpoint."
  value       = "https://${var.region}.digitaloceanspaces.com"
}

output "region" {
  value = var.region
}
