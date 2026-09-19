# `deploy/kubernetes/`

ADR-010/ADR-011's cluster half, verified against a local `kind` cluster --
**not** the ADR-010 deployment target (Hetzner, self-managed, Terraform).
This proves the manifests, Helm chart and NetworkPolicies are correct; it
proves nothing about Hetzner-specific cost, real inter-node latency, or a
self-managed control plane's operational burden.

## Bring up the local cluster

```
kind create cluster --name legion --config deploy/kubernetes/kind-cluster.yaml
```

`kind-cluster.yaml` disables kindnet, the default CNI, because it does not
enforce `NetworkPolicy` at all -- a policy would apply silently and do
nothing. Install Calico instead:

```
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.28.0/manifests/calico.yaml
```

If image pulls fail inside the kind nodes (a corporate TLS-intercepting
proxy is a common cause -- see `deploy/observability/README.md`'s note on
the same issue for the Elastic images), pre-pull on the host and load
directly instead of letting the nodes pull:

```
docker pull <image>
docker save <image> -o x.tar
docker cp x.tar <node>:/root/x.tar   # NOT /tmp -- kind nodes clear it fast
docker exec <node> ctr --namespace=k8s.io images import /root/x.tar
```

repeated per node (`legion-control-plane`, `legion-worker`, `legion-worker2`).
Build and load Legion's own nine images the same way `deploy/docker/`
already documents, then `kind load docker-image legion/<id>:dev ...` for all
of them at once (a single Legion image never hits a manifest-list issue,
unlike some third-party images, so the plain `kind load` path is fine here).

## Install KEDA

Platform infrastructure, installed once, independent of the Legion chart
itself (the same relationship the Calico CNI has to it):

```
helm repo add kedacore https://kedacore.github.io/charts
helm install keda kedacore/keda --namespace keda --create-namespace --wait
```

If pods sit `ImagePullBackOff` even after the image is loaded locally,
KEDA's chart defaults `image.pullPolicy` to `Always`, which makes kubelet
re-verify against the registry on every start regardless of what is already
imported. Fix via the chart's own value, not a raw `kubectl patch` (a patch
creates a second field manager and the next `helm upgrade` conflicts with
it):

```
helm upgrade keda kedacore/keda -n keda --set image.pullPolicy=IfNotPresent
```

## Install Legion

```
helm install legion deploy/kubernetes/helm/legion \
  -f deploy/services.yaml \
  -f deploy/kubernetes/helm/legion/values.yaml
```

`deploy/services.yaml` is passed as a values file directly -- its `services:`
key is exactly the shape the chart expects under `.Values.services`, so the
service list is never duplicated (ADR-016 section 4's own argument against a
second source of truth for the topology).

## What "verified" means here

- All 14 workloads (9 Legion services + 3 Redis + 2 Postgres) reach
  `Running`/`1/1` across five namespaces: `legion-edge`, `legion-control`,
  `legion-data`, `legion-investigation` (ADR-016 section 2's zone-to-namespace
  mapping) and `legion-state` (Zone 5, not one of Legion's own deployables).
- A real, well-formed transaction, sent to the gateway's Service through
  `kubectl port-forward`, produces a real decision through the cluster
  network -- gateway to orchestrator to velocity/device/geo/sentinel, feature
  store and dedup store both reachable.
- NetworkPolicy actually denies what it declares: a pod with no matching
  label timed out connecting to both `sentinel` and `feature-store`. The same
  pod, relabelled to match an authorised workload's selector, was then let
  through -- which is not a bug, it is ADR-011's own point that network
  policy is reachability, not authorisation; the identity layer (mTLS) is
  separate, deliberately unbuilt work.
- KEDA scales `investigation-worker` on a real Redis Streams pending-entries
  count. Verified by scaling the worker to zero, claiming a batch of
  synthetic messages under a separate consumer name in the same group
  (genuinely pending, not just present in the stream), and confirming a real
  `SuccessfulRescale` HPA event once minReplicaCount was restored.

## Known gaps

- Secret management is not built: every credential is a plain environment
  variable, the same posture `deploy/observability/`'s `.env` already has.
- ADR-011 mTLS and workload identity are untouched.
- Health probes are TCP-socket only -- no service implements
  `grpc.health.v1` yet. `investigation-controller` and `investigation-worker`
  have no probe at all: they are pure queue consumers with no listening
  port, and a probe against a port nothing binds would only ever fail.
- Nothing here pushes OTLP; the observability half (`deploy/observability/`)
  is verified as its own local stack, not wired into this one.
- Terraform for Hetzner is not written.

## Tear down

```
helm uninstall legion
helm uninstall keda -n keda
kind delete cluster --name legion
```
