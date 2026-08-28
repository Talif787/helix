# Deploys Helix to an already-running local kind cluster via the Helm chart. This is the
# free, fully applyable target: create the kind cluster and load the image (see the runbook),
# then terraform apply here installs the release. It provisions no infrastructure; it drives
# the existing cluster through the kubernetes and helm providers.

provider "kubernetes" {
  config_path    = var.kubeconfig
  config_context = var.kube_context
}

provider "helm" {
  kubernetes {
    config_path    = var.kubeconfig
    config_context = var.kube_context
  }
}

resource "helm_release" "helix" {
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
    value = var.image_repository
  }
  set {
    name  = "image.tag"
    value = var.image_tag
  }
  set {
    name  = "image.pullPolicy"
    value = "IfNotPresent"
  }
  set {
    name  = "cluster.n"
    value = var.n
  }
  set {
    name  = "cluster.r"
    value = var.r
  }
  set {
    name  = "cluster.w"
    value = var.w
  }
}
