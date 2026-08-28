# Provisions a GKE cluster, a managed node pool, and an Artifact Registry repository for the
# Helix image, then optionally deploys Helix via the Helm chart. This is the production target.
# Applying it needs a billing-enabled project with the GKE and Artifact Registry APIs enabled and
# credentials (gcloud auth application-default login). Without those you can still validate it:
#   terraform init && terraform fmt -check && terraform validate
#
# Recommended two-stage apply to avoid the provider-depends-on-resource chicken-and-egg:
#   terraform apply -var deploy_app=false     # create cluster + registry
#   <build and push the image to the registry>
#   terraform apply                            # deploy_app defaults to true

provider "google" {
  project = var.project_id
  region  = var.region
}

resource "google_artifact_registry_repository" "helix" {
  location      = var.region
  repository_id = var.artifact_repo
  description   = "Helix container images"
  format        = "DOCKER"
}

resource "google_container_cluster" "helix" {
  name     = var.cluster_name
  location = var.region

  # Manage the node pool separately (the recommended pattern), so drop the default one.
  remove_default_node_pool = true
  initial_node_count       = 1

  # Allow terraform destroy to remove the cluster. Enable this again for real production.
  deletion_protection = false
}

resource "google_container_node_pool" "helix" {
  name     = "${var.cluster_name}-pool"
  cluster  = google_container_cluster.helix.id
  location = var.region

  node_count = var.node_count

  node_config {
    machine_type = var.machine_type
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}

# Credentials for the kubernetes and helm providers, read from the created cluster.
data "google_client_config" "default" {}

provider "kubernetes" {
  host                   = "https://${google_container_cluster.helix.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.helix.master_auth[0].cluster_ca_certificate)
}

provider "helm" {
  kubernetes {
    host                   = "https://${google_container_cluster.helix.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(google_container_cluster.helix.master_auth[0].cluster_ca_certificate)
  }
}

resource "helm_release" "helix" {
  count = var.deploy_app ? 1 : 0

  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  chart            = var.chart_path

  set {
    name  = "replicaCount"
    value = var.replica_count
  }
  set {
    name  = "image.repository"
    value = "${var.region}-docker.pkg.dev/${var.project_id}/${var.artifact_repo}/helix"
  }
  set {
    name  = "image.tag"
    value = var.image_tag
  }

  depends_on = [google_container_node_pool.helix]
}
