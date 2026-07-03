{{/*
Chart name — used for the app.kubernetes.io/name label.
*/}}
{{- define "renovate-datasource.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{/*
Fully-qualified resource name. Collapses the common case where the release
name already matches the chart name (so we don't end up with resources
called "renovate-datasource-renovate-datasource"), otherwise prefixes the
chart name with the release name.
*/}}
{{- define "renovate-datasource.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Labels applied to every rendered resource. `extraLabels` from values is
merged in last so operators can override individual keys if they need to.
*/}}
{{- define "renovate-datasource.labels" -}}
app.kubernetes.io/name: {{ include "renovate-datasource.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- with .Values.extraLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Selector labels — a stable subset of the label set safe to key
Deployments and Services off.
*/}}
{{- define "renovate-datasource.selectorLabels" -}}
app.kubernetes.io/name: {{ include "renovate-datasource.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
ServiceAccount name. Users can override via .Values.serviceAccount.name,
otherwise falls back to the resource fullname.
*/}}
{{- define "renovate-datasource.serviceAccountName" -}}
{{- default (include "renovate-datasource.fullname" .) .Values.serviceAccount.name -}}
{{- end -}}

{{/*
Image reference. Prefer image.digest when set (immutable, best for
production), otherwise use image.tag. One of the two is required —
we refuse to render an implicit `latest` so operators can't
accidentally deploy a floating tag.
*/}}
{{- define "renovate-datasource.image" -}}
{{- $repo := required "image.repository is required" .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- $tag := required "image.tag or image.digest is required" .Values.image.tag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
