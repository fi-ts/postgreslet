{{/*
Expand the name of the chart.
*/}}
{{- define "postgreslet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "postgreslet.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "postgreslet.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "postgreslet.labels" -}}
helm.sh/chart: {{ include "postgreslet.chart" . }}
{{ include "postgreslet.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "postgreslet.selectorLabels" -}}
app.kubernetes.io/name: {{ include "postgreslet.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the prefix to use in (cluster)role names and bindings
*/}}
{{- define "postgreslet.roleNamePrefix" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "postgreslet.fullname" .) .Values.serviceAccount.roleNamePrefix }}
{{- else }}
{{- default "postgreslet" .Values.serviceAccount.roleNamePrefix }}
{{- end }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "postgreslet.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "postgreslet.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the custom pod security policy to use
*/}}
{{- define "postgreslet.pspName" -}}
{{- default (include "postgreslet.fullname" .) .Values.postgreslet.customPspName }}
{{- end }}
