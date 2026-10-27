{{/* Common labels applied to every object. */}}
{{- define "aintercept.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: aintercept
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* Fully-qualified image refs. */}}
{{- define "aintercept.observerImage" -}}
{{ .Values.images.observer.repository }}:{{ .Values.images.observer.tag }}
{{- end -}}

{{- define "aintercept.certsvcImage" -}}
{{ .Values.images.certsvc.repository }}:{{ .Values.images.certsvc.tag }}
{{- end -}}

{{- define "aintercept.envoyImage" -}}
{{ .Values.images.envoy.repository }}:{{ .Values.images.envoy.tag }}
{{- end -}}

{{/*
Name of the Secret holding static object-store credentials (DO/MinIO): the
user-supplied existingSecret, or the chart-created one.
*/}}
{{- define "aintercept.objectStoreSecret" -}}
{{- if .Values.objectStore.existingSecret -}}
{{ .Values.objectStore.existingSecret }}
{{- else -}}
aintercept-objectstore
{{- end -}}
{{- end -}}

{{/*
Validate required values early with a clear message rather than a confusing
runtime failure in-cluster.
*/}}
{{- define "aintercept.validate" -}}
{{- if not .Values.objectStore.bucket -}}
{{- fail "objectStore.bucket is required" -}}
{{- end -}}
{{- if not .Values.objectStore.region -}}
{{- fail "objectStore.region is required" -}}
{{- end -}}
{{- if and (not .Values.observer.keyHashSalt) (not .Values.observer.keyHashSaltSecret) -}}
{{- fail "observer.keyHashSalt (or keyHashSaltSecret) is required: without a salt, key_hash is a reversible sha256(key). Generate one: openssl rand -hex 32" -}}
{{- end -}}
{{- if eq .Values.cloud "digitalocean" -}}
{{- if not .Values.objectStore.endpoint -}}
{{- fail "objectStore.endpoint is required for cloud=digitalocean (e.g. https://nyc3.digitaloceanspaces.com)" -}}
{{- end -}}
{{- if not .Values.objectStore.existingSecret -}}
{{- if or (not .Values.objectStore.accessKeyId) (not .Values.objectStore.secretAccessKey) -}}
{{- fail "cloud=digitalocean needs object-store credentials: set objectStore.existingSecret (recommended) or objectStore.accessKeyId + secretAccessKey" -}}
{{- end -}}
{{- end -}}
{{- else if eq .Values.cloud "aws" -}}
{{- if not .Values.observer.serviceAccount.roleArn -}}
{{- fail "observer.serviceAccount.roleArn (IRSA) is required for cloud=aws" -}}
{{- end -}}
{{- else -}}
{{- fail (printf "cloud must be 'aws' or 'digitalocean', got %q" .Values.cloud) -}}
{{- end -}}
{{- end -}}
