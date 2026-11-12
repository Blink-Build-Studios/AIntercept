{{- /*
The observer container and its volumes, part of the Envoy pod: each ingress pod
is the whole ingest stack, Envoy and the observer together, scaled as one
(docs/STREAMING_PIPELINE.md, phase 1).
*/ -}}
{{- define "aintercept.spoolMaxBytes" -}}
{{- $spool := .Values.observer.spool | default dict -}}
{{- if $spool.maxBytes -}}{{ $spool.maxBytes }}{{- else -}}{{ $spool.maxBytesDefault | default 9663676416 }}{{- end -}}
{{- end -}}

{{- define "aintercept.observerContainer" -}}
- name: observer
  image: {{ include "aintercept.ainterceptImage" . }}
  imagePullPolicy: {{ .Values.images.aintercept.pullPolicy }}
  args: ["observer"]
  env:
    # ext_proc from the Envoy container in this pod, over localhost only.
    - { name: OBSERVER_GRPC_LISTEN, value: "127.0.0.1:9000" }
    - { name: OBSERVER_HTTP_LISTEN, value: "0.0.0.0:9001" }
    - { name: OBSERVER_BUFFER_SIZE, value: {{ .Values.observer.bufferSize | quote }} }
    - { name: OBSERVER_LOG_LEVEL, value: {{ .Values.observer.logLevel | default "info" | quote }} }
    # The preStop hook orders shutdown (Envoy exits first), so no delay.
    - { name: OBSERVER_SHUTDOWN_DELAY_SECONDS, value: "0" }
    # ── The writer's spool (docs/STREAMING_PIPELINE.md, phase 1) ──
    - { name: OBSERVER_SPOOL_DIR, value: /spool }
    - { name: OBSERVER_SPOOL_MAX_BYTES, value: {{ include "aintercept.spoolMaxBytes" . | quote }} }
    - { name: OBSERVER_CLIENT_IP_HEADERS, value: {{ .Values.observer.clientIPHeaders | default list | join "," | quote }} }
    - name: OBSERVER_INSTANCE
      valueFrom:
        fieldRef: { fieldPath: metadata.name }
    {{- if .Values.observer.keyHashSaltSecretsManagerId }}
    # Read from AWS Secrets Manager at startup (Pod Identity / IRSA creds).
    - { name: OBSERVER_KEY_HASH_SALT_SECRET_ID, value: {{ .Values.observer.keyHashSaltSecretsManagerId | quote }} }
    {{- else if .Values.observer.keyHashSaltSecret }}
    - name: OBSERVER_KEY_HASH_SALT
      valueFrom:
        secretKeyRef: { name: {{ .Values.observer.keyHashSaltSecret }}, key: salt }
    {{- else if .Values.observer.keyHashSalt }}
    - { name: OBSERVER_KEY_HASH_SALT, value: {{ .Values.observer.keyHashSalt | quote }} }
    {{- end }}
    # ── Object store sink ──
    - { name: OBSERVER_S3_BUCKET, value: {{ .Values.objectStore.bucket | quote }} }
    - { name: OBSERVER_S3_PREFIX, value: {{ .Values.objectStore.prefix | quote }} }
    - { name: INTERCEPTION_MODE, value: {{ include "aintercept.interceptionMode" . | quote }} }
    {{- with .Values.interceptor }}
    {{- if .endpoint }}
    # ── Interceptor (docs/INTERCEPTOR.md) ──
    - { name: INTERCEPTOR_ENDPOINT, value: {{ .endpoint | quote }} }
    - { name: INTERCEPTOR_CONTENT, value: {{ .content | toString | quote }} }
    - { name: INTERCEPTOR_DEADLINE_MS, value: {{ .deadlineMs | toString | quote }} }
    - { name: INTERCEPTOR_ON_ERROR, value: {{ .onError | quote }} }
    {{- if .tls.enabled }}
    - { name: INTERCEPTOR_TLS, value: "true" }
    {{- if .tls.serverName }}
    - { name: INTERCEPTOR_TLS_SERVER_NAME, value: {{ .tls.serverName | quote }} }
    {{- end }}
    {{- if and .tls.secretName .tls.ca }}
    - { name: INTERCEPTOR_TLS_CA_FILE, value: /etc/aintercept/interceptor-tls/ca.crt }
    {{- end }}
    {{- if and .tls.secretName .tls.clientCert }}
    - { name: INTERCEPTOR_TLS_CERT_FILE, value: /etc/aintercept/interceptor-tls/tls.crt }
    - { name: INTERCEPTOR_TLS_KEY_FILE, value: /etc/aintercept/interceptor-tls/tls.key }
    {{- end }}
    {{- end }}
    {{- if .tokenSecret }}
    - name: INTERCEPTOR_TOKEN
      valueFrom:
        secretKeyRef: { name: {{ .tokenSecret }}, key: token }
    {{- end }}
    {{- end }}
    {{- end }}
    - { name: OBSERVER_S3_REGION, value: {{ .Values.objectStore.region | quote }} }
    - { name: OBSERVER_S3_MAX_BYTES, value: {{ .Values.objectStore.maxBytes | quote }} }
    - { name: OBSERVER_S3_MAX_EVENTS, value: {{ .Values.objectStore.maxEvents | quote }} }
    - { name: OBSERVER_S3_MAX_AGE_SECONDS, value: {{ .Values.objectStore.maxAgeSeconds | quote }} }
    {{- if .Values.objectStore.endpoint }}
    - { name: OBSERVER_S3_ENDPOINT, value: {{ .Values.objectStore.endpoint | quote }} }
    {{- end }}
    {{- if .Values.objectStore.forcePathStyle }}
    - { name: OBSERVER_S3_FORCE_PATH_STYLE, value: "true" }
    {{- end }}
    # ── Unrecognized-shape sampling (structure-only; same bucket, schema-samples/ prefix) ──
    {{- $sampling := .Values.observer.schemaSampling | default dict }}
    {{- if and (hasKey $sampling "enabled") (not $sampling.enabled) }}
    - { name: OBSERVER_SCHEMA_SAMPLING, value: "false" }
    {{- end }}
    - { name: OBSERVER_SCHEMA_SAMPLES_PER_MINUTE, value: {{ $sampling.samplesPerMinute | default 120 | quote }} }
    {{- if eq .Values.cloud "digitalocean" }}
    # Static Spaces credentials (DO has no IRSA). Sourced from a Secret.
    - name: OBSERVER_S3_ACCESS_KEY_ID
      valueFrom:
        secretKeyRef: { name: {{ include "aintercept.objectStoreSecret" . }}, key: accessKeyId }
    - name: OBSERVER_S3_SECRET_ACCESS_KEY
      valueFrom:
        secretKeyRef: { name: {{ include "aintercept.objectStoreSecret" . }}, key: secretAccessKey }
    {{- end }}
    {{- with .Values.observer.memoryBudget }}
    - { name: OBSERVER_MEMORY_BUDGET, value: {{ . | quote }} }
    {{- end }}
    {{- $probe := .Values.observer.pathProbe | default dict }}
    {{- if $probe.enabled }}
    # ── Interception-path health check (/healthz/path) ──
    - { name: OBSERVER_PROBE_HOSTS, value: {{ .Values.interception.allowlist | quote }} }
    - { name: OBSERVER_PROBE_TARGET, value: {{ $probe.target | default "envoy:443" | quote }} }
    - { name: OBSERVER_PROBE_TARGETS, value: {{ join "," $probe.targets | quote }} }
    - { name: OBSERVER_PROBE_INTERVAL, value: {{ $probe.interval | default "15s" | quote }} }
    - { name: OBSERVER_PROBE_MIN_LEAF_VALIDITY, value: {{ $probe.minLeafValidity | default "30m" | quote }} }
    - { name: OBSERVER_PROBE_ROOTS, value: "/etc/aintercept/ca/ca-chain.crt" }
    {{- end }}
  ports:
    - { name: observer-http, containerPort: 9001 }
  lifecycle:
    # Returns once Envoy (this pod's other container) has exited, so the
    # observer records every stream Envoy serves while draining and only
    # then gets SIGTERM: GracefulStop, drain, flush the writer, exit.
    preStop:
      httpGet: { path: /prestop, port: 9001 }
  readinessProbe:
    httpGet: { path: /healthz/ready, port: 9001 }
    initialDelaySeconds: 2
    periodSeconds: 5
  livenessProbe:
    httpGet: { path: /healthz, port: 9001 }
    initialDelaySeconds: 5
    periodSeconds: 10
  resources:
    {{- toYaml .Values.observer.resources | nindent 12 }}
  securityContext:
    runAsUser: 65532
    runAsGroup: 65532
    allowPrivilegeEscalation: false
    readOnlyRootFilesystem: true
    capabilities: { drop: ["ALL"] }
  volumeMounts:
    - { name: spool, mountPath: /spool }
    {{- if $probe.enabled }}
    - { name: ca-chain, mountPath: /etc/aintercept/ca, readOnly: true }
    {{- end }}
    {{- $icTLS := and .Values.interceptor.endpoint .Values.interceptor.tls.enabled .Values.interceptor.tls.secretName }}
    {{- if $icTLS }}
    - { name: interceptor-tls, mountPath: /etc/aintercept/interceptor-tls, readOnly: true }
    {{- end }}
{{- end -}}

{{- define "aintercept.observerVolumes" -}}
{{- $probe := .Values.observer.pathProbe | default dict }}
{{- $icTLS := and .Values.interceptor.endpoint .Values.interceptor.tls.enabled .Values.interceptor.tls.secretName }}
# The writer's spool: sealed files wait here until they're uploaded. It
# outlives container restarts (an OOM or crash), so the restarted observer
# uploads what the last one sealed.
- name: spool
  emptyDir:
    sizeLimit: {{ (.Values.observer.spool | default dict).sizeLimit | default "10Gi" }}
{{- if $probe.enabled }}
# Only the public chain is projected; the intermediate key in the same Secret
# is never mounted into the observer.
- name: ca-chain
  secret:
    secretName: {{ .Values.interception.caSecretName }}
    items:
      - { key: ca-chain.crt, path: ca-chain.crt }
{{- end }}
{{- if $icTLS }}
- name: interceptor-tls
  secret:
    secretName: {{ .Values.interceptor.tls.secretName }}
{{- end }}
{{- end -}}
