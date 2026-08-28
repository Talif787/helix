output "release_name" {
  description = "The installed Helm release name."
  value       = helm_release.helix.name
}

output "namespace" {
  description = "The namespace the release was installed into."
  value       = helm_release.helix.namespace
}

output "release_status" {
  description = "The Helm release status."
  value       = helm_release.helix.status
}
