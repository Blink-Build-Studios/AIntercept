{{/* Common labels applied to every object. */}}
{{- define "aintercept.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: aintercept
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/*
Fully-qualified image ref: repository@digest when a digest is set (immutable;
what production deploys use), else repository:tag.
*/}}
{{- define "aintercept.image" -}}
{{- if .digest -}}
{{ .repository }}@{{ .digest }}
{{- else -}}
{{ .repository }}:{{ .tag }}
{{- end -}}
{{- end -}}

{{- define "aintercept.ainterceptImage" -}}
{{ include "aintercept.image" .Values.images.aintercept }}
{{- end -}}

{{- define "aintercept.apiImage" -}}
{{ include "aintercept.image" .Values.images.api }}
{{- end -}}

{{- define "aintercept.envoyImage" -}}
{{ include "aintercept.image" .Values.images.envoy }}
{{- end -}}

{{/* The Envoy config, rendered (it carries chart conditionals). */}}
{{/*
The resolvers Envoy uses for the REAL provider hostnames, one cares
socket_address per entry. Never point these at anything that sees the
client-side DNS override, or traffic loops back into the proxy.
*/}}
{{- define "aintercept.upstreamResolvers" -}}
{{- $r := .Values.envoy.upstreamDnsResolvers | default (list "1.1.1.1" "8.8.8.8") -}}
{{- range $i, $addr := $r }}
{{- if $i }}{{ "\n" }}{{ end -}}
- socket_address: { address: {{ $addr | quote }}, port_value: 53 }
{{- end }}
{{- end -}}

{{/*
Scheduling fields for a pod spec, from a component's values block:
nodeSelector, tolerations, affinity, topologySpreadConstraints. Each renders
only when set, so an unset block leaves the pod spec unchanged.
*/}}
{{- define "aintercept.scheduling" -}}
{{- with .nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* ServiceAccount name for process or api: serviceAccount.name, else aintercept-<component>. */}}
{{- define "aintercept.readerServiceAccountName" -}}
{{- $sa := .c.serviceAccount | default dict -}}
{{ $sa.name | default (printf "aintercept-%s" .component) }}
{{- end -}}

{{- define "aintercept.envoyConfig" -}}
{{- include "aintercept.checkAllowlist" . -}}
{{ tpl (.Files.Get "files/envoy.multi.yaml") . }}
{{- end -}}

{{/*
Fail the render when interception.allowlist and the hosts Envoy serves differ.
Envoy waits for every host's certificate before it goes ready, so a host Envoy
serves but certsvc won't mint would keep every Envoy pod unready.
*/}}
{{- define "aintercept.checkAllowlist" -}}
{{- $envoy := list -}}
{{- range regexFindAll "- name: \"[^\"]+\"\\s+sds_config" (.Files.Get "files/envoy.multi.yaml") -1 -}}
{{- $envoy = append $envoy (regexReplaceAll "- name: \"([^\"]+)\"[\\s\\S]*" . "${1}") -}}
{{- end -}}
{{- $allow := list -}}
{{- range splitList "," .Values.interception.allowlist -}}
{{- $allow = append $allow (trim . | lower) -}}
{{- end -}}
{{- range $envoy -}}
{{- if not (has . $allow) -}}
{{- fail (printf "interception.allowlist is missing %q, which Envoy serves (files/envoy.multi.yaml): Envoy would wait for its certificate forever" .) -}}
{{- end -}}
{{- end -}}
{{- range $allow -}}
{{- if not (has . $envoy) -}}
{{- fail (printf "interception.allowlist has %q, which Envoy doesn't serve: add it to files/envoy.multi.yaml (filter chain, SDS secret, virtual host, clusters)" .) -}}
{{- end -}}
{{- end -}}
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
{{- with .Values.interceptor -}}
{{- if and .enforce (not .endpoint) -}}
{{- fail "interceptor.enforce needs interceptor.endpoint: enforced calls go through the Interceptor" -}}
{{- end -}}
{{- if and .enforce (lt (int $.Values.observer.autoscaling.minReplicas) 2) -}}
{{- fail "interceptor.enforce needs observer.autoscaling.minReplicas >= 2: enforced calls are refused while no observer is up" -}}
{{- end -}}
{{- if not (has (.onError | default "deny") (list "deny" "allow")) -}}
{{- fail "interceptor.onError must be deny or allow" -}}
{{- end -}}
{{- range .enforce -}}
{{- if not (has .host (splitList "," (nospace $.Values.interception.allowlist))) -}}
{{- fail (printf "interceptor.enforce host %q isn't in interception.allowlist" .host) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if not .Values.objectStore.bucket -}}
{{- fail "objectStore.bucket is required" -}}
{{- end -}}
{{- if not .Values.objectStore.region -}}
{{- fail "objectStore.region is required" -}}
{{- end -}}
{{- if not (or .Values.observer.keyHashSalt .Values.observer.keyHashSaltSecret .Values.observer.keyHashSaltSecretsManagerId) -}}
{{- fail "a key-hash salt is required (observer.keyHashSaltSecretsManagerId, keyHashSaltSecret, or keyHashSalt): without a salt, key_hash is a reversible sha256(key). Generate one: openssl rand -hex 32" -}}
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
{{- /* AWS credentials come from EKS Pod Identity (an association made outside
the cluster; nothing to set here) or IRSA (serviceAccount.roleArn). */ -}}
{{- else -}}
{{- fail (printf "cloud must be 'aws' or 'digitalocean', got %q" .Values.cloud) -}}
{{- end -}}
{{- end -}}

{{/* The interception mode, validated: content-free (default) or full. */}}
{{- define "aintercept.interceptionMode" -}}
{{- $m := .Values.interceptionMode | default "content-free" -}}
{{- if not (has $m (list "content-free" "full")) -}}
{{- fail (printf "interceptionMode must be content-free or full, not %q" $m) -}}
{{- end -}}
{{- $m -}}
{{- end }}

{{/*
Enforced routes for one virtual host (interceptor.enforce): for each path
prefix in scope (the whole host when no paths are listed), a WebSocket upgrade
route and a default route through the enforcing filter, ahead of the host's
observe routes. Empty when the host isn't in an enforce scope.
*/}}
{{- define "aintercept.enforceRoutes" -}}
{{- $root := . -}}
{{- range ((.values.interceptor | default dict).enforce | default list) }}
{{- if eq .host $root.host }}
{{- range (.paths | default (list "/")) }}
{{- if $root.ws }}
- match:
    prefix: {{ . | quote }}
    headers: [{ name: upgrade, string_match: { exact: websocket, ignore_case: true } }]
  route: { cluster: {{ $root.ws }}, timeout: 0s, idle_timeout: 300s }
  typed_per_filter_config:
    envoy.filters.http.ext_proc:
      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
      disabled: true
    aintercept.enforce:
      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
      overrides: {}
{{- end }}
- match: { prefix: {{ . | quote }} }
  route: { cluster: {{ $root.cluster }}, timeout: 0s, idle_timeout: 300s }
  typed_per_filter_config:
    envoy.filters.http.ext_proc:
      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
      disabled: true
    aintercept.enforce:
      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
      overrides: {}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
