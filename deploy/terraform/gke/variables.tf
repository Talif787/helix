variable "project_id" {
  description = "GCP project id. Must have billing enabled and the container.googleapis.com and artifactregistry.googleapis.com APIs enabled."
  type        = string
}

variable "region" {
  description = "GCP region for the cluster and the Artifact Registry repository."
  type        = string
  default     = "us-central1"
}

variable "cluster_name" {
  description = "GKE cluster name."
  type        = string
  default     = "helix"
}

variable "node_count" {
  description = "Nodes per zone in the managed node pool."
  type        = number
  default     = 1
}

variable "machine_type" {
  description = "Node machine type."
  type        = string
  default     = "e2-standard-2"
}

variable "artifact_repo" {
  description = "Artifact Registry repository id for the Helix image."
  type        = string
  default     = "helix"
}

variable "image_tag" {
  description = "Image tag to deploy (must already be pushed to the registry)."
  type        = string
  default     = "latest"
}

variable "release_name" {
  description = "Helm release name."
  type        = string
  default     = "helix"
}

variable "namespace" {
  description = "Namespace for the release."
  type        = string
  default     = "helix"
}

variable "chart_path" {
  description = "Path to the Helix Helm chart, relative to this directory."
  type        = string
  default     = "../../helm/helix"
}

variable "replica_count" {
  description = "Number of Helix nodes."
  type        = number
  default     = 3
}

variable "deploy_app" {
  description = "Whether to deploy the Helix Helm release after the cluster exists. Set false for the recommended two-stage apply: create the cluster first, push the image, then apply again with true."
  type        = bool
  default     = true
}
