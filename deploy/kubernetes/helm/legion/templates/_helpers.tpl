{{/*
Namespace a service belongs to: Zone -> namespace is ADR-016's own mapping
("legion-" + zone), not something this chart invents.
*/}}
{{- define "legion.namespace" -}}
legion-{{ .zone }}
{{- end -}}

{{/*
A service's cluster-internal address, "id.legion-zone.svc.cluster.local:port".
*/}}
{{- define "legion.fqdn" -}}
{{ .id }}.legion-{{ .svc.zone }}.svc.cluster.local:{{ .svc.port }}
{{- end -}}

{{/*
Standard labels, applied to every object this chart creates.
*/}}
{{- define "legion.labels" -}}
app.kubernetes.io/part-of: legion
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels for one service -- ADR-016 section 1 fixes the identifier as
the label selector value.
*/}}
{{- define "legion.selectorLabels" -}}
app.kubernetes.io/name: {{ .id }}
{{- end -}}

{{/*
LEGION_AGENT_ENDPOINTS: "id=fqdn:port,..." for every enabled, non-planned
agent -- the same string shape control-plane/orchestrator/main.go's
defaultAgentEndpoints constant uses, built here instead of hand-copied so it
can never drift from deploy/services.yaml.
*/}}
{{- define "legion.agentEndpoints" -}}
{{- $pairs := list -}}
{{- range $id, $svc := .Values.services -}}
{{- if and $svc.agent (not $svc.planned) -}}
{{- $pairs = append $pairs (printf "%s=%s.legion-%s.svc.cluster.local:%v" $id $id $svc.zone $svc.port) -}}
{{- end -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}

{{/*
DNS egress, needed by every workload that resolves a Service name -- which
is everything except legion-data, which resolves nothing because it calls
nothing (Zone 3 makes no outbound calls of any kind, ADR-002/006/013).
*/}}
{{- define "legion.dnsEgressRule" -}}
to:
  - namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: kube-system
ports:
  - protocol: UDP
    port: 53
  - protocol: TCP
    port: 53
{{- end -}}
