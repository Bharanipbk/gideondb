{{- define "gideondb.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gideondb.fullname" -}}
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

{{- define "gideondb.headless" -}}
{{- printf "%s-headless" (include "gideondb.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gideondb.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gideondb.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gideondb.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "gideondb.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "gideondb.secretName" -}}
{{- default (include "gideondb.fullname" .) .Values.auth.existingSecret -}}
{{- end -}}

{{- define "gideondb.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "gideondb.replicationFactor" -}}
{{- if gt (int .Values.cluster.replicationFactor) 0 -}}
{{- .Values.cluster.replicationFactor -}}
{{- else -}}
{{- min (int .Values.replicaCount) 3 -}}
{{- end -}}
{{- end -}}

{{/* Stable per-ordinal peer URLs on the headless Service. */}}
{{- define "gideondb.peers" -}}
{{- $peers := list -}}
{{- $fullname := include "gideondb.fullname" . -}}
{{- $headless := include "gideondb.headless" . -}}
{{- range $i := until (int .Values.replicaCount) -}}
{{- $peers = append $peers (printf "https://%s-%d.%s.%s.svc.%s:%d" $fullname $i $headless $.Release.Namespace $.Values.clusterDomain (int $.Values.service.port)) -}}
{{- end -}}
{{- join "," $peers -}}
{{- end -}}

{{/* Fail early on combinations the server would reject at startup. */}}
{{- define "gideondb.validate" -}}
{{- if lt (int .Values.replicaCount) 1 -}}
{{- fail "replicaCount must be at least 1" -}}
{{- end -}}
{{- if not (has .Values.tls.mode (list "selfSigned" "secret")) -}}
{{- fail "tls.mode must be selfSigned or secret (the server requires HTTPS for non-loopback dashboard sign-in)" -}}
{{- end -}}
{{- if and (eq .Values.tls.mode "secret") (not .Values.tls.secretName) -}}
{{- fail "tls.mode=secret requires tls.secretName" -}}
{{- end -}}
{{- if and (gt (int .Values.replicaCount) 1) (ne .Values.tls.mode "secret") -}}
{{- fail "replicaCount > 1 requires tls.mode=secret with a private CA (ca.crt) so nodes can verify each other" -}}
{{- end -}}
{{- if gt (int (include "gideondb.replicationFactor" .)) (int .Values.replicaCount) -}}
{{- fail "cluster.replicationFactor cannot exceed replicaCount" -}}
{{- end -}}
{{- if and .Values.auth.apiKey (lt (len .Values.auth.apiKey) 16) -}}
{{- fail "auth.apiKey must contain at least 16 characters" -}}
{{- end -}}
{{- if and .Values.cluster.id (not (regexMatch "^[0-9a-f]{32}$" .Values.cluster.id)) -}}
{{- fail "cluster.id must be 32 lowercase hexadecimal characters" -}}
{{- end -}}
{{- end -}}
