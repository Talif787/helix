variable "kubeconfig" {
  description = "Path to the kubeconfig file."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "Kube context to target (the local kind cluster)."
  type        = string
  default     = "kind-helix-test"
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

variable "image_repository" {
  description = "Container image repository."
  type        = string
  default     = "helix"
}

variable "image_tag" {
  description = "Container image tag (the image loaded into kind)."
  type        = string
  default     = "local"
}

variable "n" {
  description = "Replication factor."
  type        = number
  default     = 3
}

variable "r" {
  description = "Read quorum."
  type        = number
  default     = 2
}

variable "w" {
  description = "Write quorum."
  type        = number
  default     = 2
}
