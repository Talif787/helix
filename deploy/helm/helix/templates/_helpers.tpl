{{/* Chart name, optionally overridden. */}}
{{- define "helix.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name; becomes the StatefulSet name and the pod-name prefix. */}}
{{- define "helix.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Headless Service name used for stable per-pod DNS. */}}
{{- define "helix.headlessName" -}}
{{- printf "%s-headless" (include "helix.fullname" .) -}}
{{- end -}}

{{- define "helix.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "helix.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{- define "helix.selectorLabels" -}}
app.kubernetes.io/name: {{ include "helix.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Build HELIX_PEERS for exactly replicaCount pods. Each entry is
  <fullname>-<i>=<fullname>-<i>.<headless>.<namespace>.svc.<clusterDomain>:<grpcPort>
The pod's own id (HELIX_NODE_ID, from metadata.name) equals <fullname>-<i>, so every node's id is
present in this list, which the binary requires.
*/}}
{{- define "helix.peers" -}}
{{- $full := include "helix.fullname" . -}}
{{- $svc := include "helix.headlessName" . -}}
{{- $domain := .Values.clusterDomain -}}
{{- $ns := .Release.Namespace -}}
{{- $port := int .Values.ports.grpc -}}
{{- $peers := list -}}
{{- range $i := until (int .Values.replicaCount) -}}
{{- $peers = append $peers (printf "%s-%d=%s-%d.%s.%s.svc.%s:%d" $full $i $full $i $svc $ns $domain $port) -}}
{{- end -}}
{{- join "," $peers -}}
{{- end -}}
