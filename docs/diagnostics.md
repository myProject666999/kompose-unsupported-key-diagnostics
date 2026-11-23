---
layout: default
title: Degradation Diagnostics
permalink: /diagnostics/
redirect_from:
  - /docs/diagnostics.md/
  - /docs/diagnostics/
---

# Degradation Diagnostics

* TOC
{:toc}

Converting a Compose file to Kubernetes or OpenShift is not always lossless. When
kompose cannot fully honour a key it now emits **one structured line per
degradation** instead of an ad-hoc warning at the site where the loss happens.
Every line uses the same format, so it can be grepped in CI and aggregated by
category.

## Line format

Every diagnostic is a single line beginning with `[kompose] `:

```
[kompose] category=<category> key=<key> [services=<a,b,c>] [original="..."] [converted="..."] [condition="..."] [origin=<origin>] [location=<code>] message="..."
```

Fields appear in this fixed order and optional fields are omitted when empty.
Service names are sorted and quoted values use Go string quoting, so the same
input always produces byte-identical output.

## Categories

| Category | Meaning | Extra fields |
|----------|---------|--------------|
| `ignored` | The key leaves no trace in the output (for example `ulimits`, `sysctls`, `devices`). | `services`, `original` |
| `approximated` | The key is converted but the semantics differ (for example `restart: unless-stopped` becomes `always`, `deploy.endpoint_mode: vip` becomes `NodePort`). | `services`, `original`, `converted` |
| `conditional` | The key is only supported for specific values (for example `links` requires the service name and alias to match; `network_mode` only supports `service:<name>`). | `services`, `original`, `condition` |
| `rejected` | Conversion cannot continue. | `services`, `origin`, `location` |

`rejected` diagnostics additionally carry an `origin`:

- `invalid-input` — the Compose file itself must change (for example a
  `kompose.service.nodeport.port` without `kompose.service.type: nodeport`).
- `unsupported` — the input is valid Compose but kompose cannot convert it (for
  example an OpenShift DeploymentConfig combined with `restart: on-failure`).

## Aggregation

Diagnostics are grouped by `(category, key, message)`. A key used by ten
services becomes **one** line listing the services, sorted alphabetically. When
more than 10 services share a diagnostic the list is truncated after the first
10 sorted names and the line states the total:

```
services=a,b,c,d,e,f,g,h,i,j (+2 more, total=12)
```

## Exit codes and `--strict`

By default diagnostics are advisory and never change the exit code. Rejected
diagnostics abort conversion as before.

Pass `--strict` (or use the library option `client.WithStrict()`) to exit
non-zero when any `ignored` or `approximated` diagnostic is produced.
`conditional` diagnostics do not fail strict mode because the key is supported
for the documented values. `--strict` never hides or weakens rejected
diagnostics.

`--suppress-warnings` lowers the log level so `ignored`, `approximated` and
`conditional` lines disappear, but `rejected` lines are logged at error level
and remain visible.

## Service keys

The table below is generated from the registry in
`pkg/loader/compose/unsupported.go`. The `TestKeyRegistryMatchesImplementation`
test builds a minimal Compose file for every entry and asserts that a diagnostic
is actually produced, so the list cannot drift from the conversion code.

### Ignored (55)

| | | | | |
|---|---|---|---|---|
| annotations | attach | blkio_config | cgroup | cgroup_parent |
| cpu_count | cpu_percent | cpu_period | cpu_quota | cpu_rt_period |
| cpu_rt_runtime | cpu_shares | cpus | cpuset | credential_spec |
| depends_on | deploy.rollback_config | develop | device_cgroup_rules | devices |
| dns | dns_opt | dns_search | external_links | extra_hosts |
| gpus | init | ipc | isolation | logging |
| mac_address | mem_reservation | mem_swappiness | memswap_limit | models |
| oom_kill_disable | oom_score_adj | pids_limit | platform | post_start |
| pre_stop | provider | pull_policy | runtime | scale |
| security_opt | shm_size | stop_signal | storage_opt | sysctls |
| ulimits | use_api_socket | userns_mode | uts | volumes_from |

`depends_on` only reports entries that the user wrote explicitly. Entries that
compose-go derives automatically from `links`, `network_mode: service:...` or
`volumes_from` are not reported.

### Approximated (2)

| key | original | converted |
|-----|----------|-----------|
| `restart` | `unless-stopped` | `always` |
| `deploy.endpoint_mode` | `vip` | `NodePort` |

`deploy.endpoint_mode` with any other value (for example `dnsrr`) leaves no trace
and is reported as `ignored` instead.

### Conditional (2)

| key | condition |
|-----|-----------|
| `links` | entries must be `SERVICE` or `SERVICE:SERVICE`; `SERVICE:ALIAS` is not supported |
| `network_mode` | only the `service:<name>` form is supported |

Keys that kompose fully converts are deliberately **absent** from the registry.
This includes `read_only` (mapped to `securityContext.readOnlyRootFilesystem`)
and `env_file` (compose-go v2 names the field `EnvFiles`; the old hard-coded list
used the v1 name `EnvFile` and matched nothing).

## Root-level objects

Root-level `volumes`, `networks`, `secrets` and `configs` are reported per
object, for example `volumes.data.driver` or `secrets.ext.external`. Only the
volume labels `kompose.volume.size` and `kompose.volume.selector` are read.

## Extension fields

Fields starting with `x-` are part of the Compose specification and are ignored
without a diagnostic.

## Rejected-key inventory

The following rejections currently stop conversion. They are listed so a later
change can decide which `log.Fatalf` calls are worth converting into returned
errors. All of them are represented by the shared `rejected` category.

| Location | Trigger | Current handling | Category | Origin |
|----------|---------|------------------|----------|--------|
| `pkg/loader/compose/compose.go:parseKomposeLabels` | `kompose.service.nodeport.port` set while the service type is not `nodeport` | returns an error | rejected | invalid-input |
| `pkg/loader/compose/compose.go:parseKomposeLabels` | `kompose.service.nodeport.port` set on a service with multiple ports | returns an error | rejected | invalid-input |
| `pkg/loader/compose/compose.go:parseKomposeLabels` | `kompose.service.expose.tls-secret` or `...ingress-class-name` without `kompose.service.expose` | returns an error | rejected | invalid-input |
| `pkg/transformer/openshift/openshift.go:Transform` | DeploymentConfig combined with `restart: on-failure`/`no` | returns an error | rejected | unsupported |
| `pkg/transformer/kubernetes/kubernetes.go:ConfigServicePorts` | LoadBalancer service using TCP and UDP on the same port | `log.Fatalf` | rejected | unsupported |
| `pkg/transformer/kubernetes/kubernetes.go:InitConfigMapForEnv` | env file cannot be read while building a ConfigMap | `log.Fatalf` | rejected | unsupported |

This change records the first four cases in the diagnostic report. The two
`log.Fatalf` cases keep their process-exit behaviour for compatibility but now
also record a `rejected` diagnostic before exiting; converting them to returned
errors is left for a follow-up.

## Provider differences

The Compose service-key registry is shared by the Kubernetes and OpenShift
transformers, so those lines are identical for both providers. OpenShift has an
additional provider-specific list (currently empty) whose keys are named with
the `LoadedFrom` label; a key added there would render as:

```
[kompose] category=ignored key=<label> message="OpenShift provider doesn't support <label> key - ignoring"
```

The only OpenShift-specific rejection today is the DeploymentConfig /
`restart: on-failure` conflict, which is reported as
`category=rejected key=restart origin=unsupported` with the code location
`pkg/transformer/openshift/openshift.go:Transform`.
