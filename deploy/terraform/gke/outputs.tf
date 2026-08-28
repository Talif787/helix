output "cluster_name" {
  description = "The GKE cluster name."
  value       = google_container_cluster.helix.name
}

output "cluster_endpoint" {
  description = "The GKE control plane endpoint."
  value       = google_container_cluster.helix.endpoint
  sensitive   = true
}

output "artifact_registry" {
  description = "The Docker repository to push the Helix image to."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${var.artifact_repo}"
}

output "get_credentials_command" {
  description = "Run this to point kubectl at the new cluster."
  value       = "gcloud container clusters get-credentials ${var.cluster_name} --region ${var.region} --project ${var.project_id}"
}
