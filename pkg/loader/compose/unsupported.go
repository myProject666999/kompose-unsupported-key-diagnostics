/*
Copyright 2017 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package compose

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kubernetes/kompose/pkg/diagnostic"
)

// This file is the single source of truth for how compose keys degrade during
// conversion. Every entry is data: the yaml key the user writes, the
// compose-go field that proves the key is present, the degradation category,
// and the explanation.
//
// A key only belongs here if the conversion code does not fully honour it.
// Keys that are converted (read_only, env_file, ports, ...) must NOT appear;
// otherwise the user gets a warning for working configuration. The
// TestKeyRegistryMatchesImplementation test enforces the invariant in both
// directions for every entry.
//
// The registry intentionally covers only keys the compose-go schema accepts.
// Legacy keys such as "net" and "volume_driver" are rejected by the schema
// before conversion starts, so they can never reach a diagnostic here.

// keySpec describes one degrading compose service key.
type keySpec struct {
	// YAMLKey is the key as written in the Compose file.
	YAMLKey string
	// GoField is the compose-go ServiceConfig field that carries the key.
	// TestKeyRegistryMatchesImplementation asserts the field still exists so
	// a compose-go rename (for example EnvFile -> EnvFiles) cannot silently
	// break detection.
	GoField string
	// Category is the degradation class.
	Category diagnostic.Category
	// Message is the user-facing explanation.
	Message string
	// Condition is required for Conditional keys.
	Condition string
	// detect overrides the default "field is non-zero" detection for keys
	// whose support depends on the value.
	detect func(types.ServiceConfig) (original string, triggered bool)
	// categoryFor optionally refines the category per value. It is used for
	// deploy.endpoint_mode, where vip is approximated but dnsrr is ignored.
	categoryFor func(types.ServiceConfig) diagnostic.Category
}

const ignoredMessage = "key is not supported by Kubernetes and is ignored"

// composeServiceKeySpecs is the registry. Keep it sorted by YAMLKey.
var composeServiceKeySpecs = []keySpec{
	{YAMLKey: "annotations", GoField: "Annotations", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "attach", GoField: "Attach", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "blkio_config", GoField: "BlkioConfig", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cgroup", GoField: "Cgroup", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cgroup_parent", GoField: "CgroupParent", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_count", GoField: "CPUCount", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_percent", GoField: "CPUPercent", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_period", GoField: "CPUPeriod", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_quota", GoField: "CPUQuota", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_rt_period", GoField: "CPURTPeriod", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_rt_runtime", GoField: "CPURTRuntime", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpu_shares", GoField: "CPUShares", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpus", GoField: "CPUS", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "cpuset", GoField: "CPUSet", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "credential_spec", GoField: "CredentialSpec", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "depends_on", GoField: "DependsOn", Category: diagnostic.Ignored,
		Message: "depends_on is not converted: Kubernetes starts all containers in a pod together",
		detect:  detectExplicitDependsOn},
	{YAMLKey: "develop", GoField: "Develop", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "device_cgroup_rules", GoField: "DeviceCgroupRules", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "devices", GoField: "Devices", Category: diagnostic.Ignored,
		Message: "devices is not supported within Kubernetes, See issue https://github.com/kubernetes/kubernetes/issues/5607"},
	{YAMLKey: "dns", GoField: "DNS", Category: diagnostic.Ignored,
		Message: "dns is not used within Kubernetes, Kubernetes uses a managed DNS server"},
	{YAMLKey: "dns_opt", GoField: "DNSOpts", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "dns_search", GoField: "DNSSearch", Category: diagnostic.Ignored,
		Message: "dns_search is not used within Kubernetes, Kubernetes uses a managed DNS server"},
	{YAMLKey: "deploy.endpoint_mode", GoField: "Deploy", Category: diagnostic.Approximated,
		Message: "deploy.endpoint_mode is not supported; the Service type is forced to NodePort",
		detect:  detectEndpointMode,
		// Only "vip" changes the output (Service type becomes NodePort).
		// "dnsrr" and any other value leave no trace.
		categoryFor: func(svc types.ServiceConfig) diagnostic.Category {
			if svc.Deploy != nil && svc.Deploy.EndpointMode == "vip" {
				return diagnostic.Approximated
			}
			return diagnostic.Ignored
		}},
	{YAMLKey: "deploy.rollback_config", GoField: "Deploy", Category: diagnostic.Ignored, Message: ignoredMessage,
		detect: detectRollbackConfig},
	{YAMLKey: "external_links", GoField: "ExternalLinks", Category: diagnostic.Ignored,
		Message: "external_links has no 1-1 conversion: Kubernetes uses a flat network for all containers"},
	{YAMLKey: "extra_hosts", GoField: "ExtraHosts", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "gpus", GoField: "Gpus", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "init", GoField: "Init", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "ipc", GoField: "Ipc", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "isolation", GoField: "Isolation", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "links", GoField: "Links", Category: diagnostic.Conditional,
		Message:   "links is only supported when the service name and alias are identical",
		Condition: "links entries must be SERVICE or SERVICE:SERVICE; SERVICE:ALIAS is not supported",
		detect:    detectUnsupportedLinks},
	{YAMLKey: "logging", GoField: "Logging", Category: diagnostic.Ignored,
		Message: "logging is not converted: Kubernetes has built-in node-level logging"},
	{YAMLKey: "mac_address", GoField: "MacAddress", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "mem_reservation", GoField: "MemReservation", Category: diagnostic.Ignored,
		Message: "mem_reservation is not converted; use deploy.resources.reservations instead"},
	{YAMLKey: "mem_swappiness", GoField: "MemSwappiness", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "memswap_limit", GoField: "MemSwapLimit", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "models", GoField: "Models", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "network_mode", GoField: "NetworkMode", Category: diagnostic.Conditional,
		Message:   "network_mode is only supported in the form 'service:<name>'",
		Condition: "network_mode must be service:<name>",
		detect:    detectUnsupportedNetworkMode},
	{YAMLKey: "oom_kill_disable", GoField: "OomKillDisable", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "oom_score_adj", GoField: "OomScoreAdj", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "pids_limit", GoField: "PidsLimit", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "platform", GoField: "Platform", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "post_start", GoField: "PostStart", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "pre_stop", GoField: "PreStop", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "provider", GoField: "Provider", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "pull_policy", GoField: "PullPolicy", Category: diagnostic.Ignored,
		Message: "pull_policy is not converted; use the kompose.image-pull-policy label instead"},
	{YAMLKey: "restart", GoField: "Restart", Category: diagnostic.Approximated,
		Message: "Restart policy 'unless-stopped' is not supported, convert it to 'always'",
		detect:  detectUnlessStoppedRestart},
	{YAMLKey: "runtime", GoField: "Runtime", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "scale", GoField: "Scale", Category: diagnostic.Ignored,
		Message: "scale is not converted; use deploy.replicas or --replicas instead"},
	{YAMLKey: "security_opt", GoField: "SecurityOpt", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "shm_size", GoField: "ShmSize", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "storage_opt", GoField: "StorageOpt", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "stop_signal", GoField: "StopSignal", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "sysctls", GoField: "Sysctls", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "ulimits", GoField: "Ulimits", Category: diagnostic.Ignored,
		Message: "ulimits is not supported within Kubernetes, See issue https://github.com/kubernetes/kubernetes/issues/3595"},
	{YAMLKey: "userns_mode", GoField: "UserNSMode", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "use_api_socket", GoField: "UseAPISocket", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "uts", GoField: "Uts", Category: diagnostic.Ignored, Message: ignoredMessage},
	{YAMLKey: "volumes_from", GoField: "VolumesFrom", Category: diagnostic.Ignored,
		Message: "volumes_from is not converted; define the volumes on each service instead"},
}

// collectUnsupportedKeys walks all services and records one diagnostic per
// degrading key. Extension fields (x-*) are part of the Compose specification
// and are never reported.
func collectUnsupportedKeys(project *types.Project, report *diagnostic.Report) {
	if project == nil {
		return
	}
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := project.Services[name]
		for _, spec := range composeServiceKeySpecs {
			original, triggered := spec.evaluate(svc)
			if !triggered {
				continue
			}
			category := spec.Category
			if spec.categoryFor != nil {
				category = spec.categoryFor(svc)
			}
			message := spec.Message
			if category != spec.Category && category == diagnostic.Ignored {
				message = ignoredMessage
			}
			report.Add(diagnostic.Diagnostic{
				Category:  category,
				Service:   svc.Name,
				Key:       spec.YAMLKey,
				Original:  original,
				Converted: spec.convertedFor(svc),
				Condition: spec.Condition,
				Message:   message,
			})
		}
	}
	collectRootDiagnostics(project, report)
}

// evaluate applies the spec's detection rule.
func (s keySpec) evaluate(svc types.ServiceConfig) (string, bool) {
	if s.detect != nil {
		return s.detect(svc)
	}
	if s.GoField == "" {
		return "", false
	}
	v := reflect.ValueOf(svc).FieldByName(s.GoField)
	if !v.IsValid() || isZeroReflect(v) {
		return "", false
	}
	return summarizeReflect(v), true
}

// convertedFor returns the value kompose produces for approximated keys.
func (s keySpec) convertedFor(svc types.ServiceConfig) string {
	if s.Category != diagnostic.Approximated {
		return ""
	}
	switch s.YAMLKey {
	case "restart":
		if effectiveRestart(svc) == "unless-stopped" {
			return "always"
		}
	case "deploy.endpoint_mode":
		if svc.Deploy != nil && svc.Deploy.EndpointMode == "vip" {
			return "NodePort"
		}
	}
	return ""
}

// isZeroReflect treats nil and empty maps/slices as absent. The reflection
// based checks used before this registry treated an explicitly written
// `ulimits: {}` as present because it only special-cased slices; here both
// empty maps and empty slices count as zero.
func isZeroReflect(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Map, reflect.Slice:
		return v.IsNil() || v.Len() == 0
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return v.IsZero()
	default:
		return v.IsZero()
	}
}

// summarizeReflect renders a short deterministic summary of a value.
func summarizeReflect(v reflect.Value) string {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return fmt.Sprint(v.Interface())
	case reflect.Slice, reflect.Array:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, summarizeReflect(v.Index(i)))
		}
		return truncate(strings.Join(parts, ","), 120)
	case reflect.Map:
		keys := v.MapKeys()
		if len(keys) == 0 {
			return ""
		}
		strKeys := make([]string, 0, len(keys))
		for _, k := range keys {
			strKeys = append(strKeys, fmt.Sprint(k.Interface()))
		}
		sort.Strings(strKeys)
		parts := make([]string, 0, len(strKeys))
		for _, k := range strKeys {
			parts = append(parts, k+"="+summarizeReflect(v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key()))))
		}
		return truncate(strings.Join(parts, ","), 120)
	case reflect.Struct:
		// Render only the non-zero exported scalar fields of simple structs
		// such as compose-go's UlimitsConfig, so the summary is readable
		// instead of showing pointer addresses or nested maps.
		t := v.Type()
		parts := make([]string, 0, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			fv := v.Field(i)
			if isZeroReflect(fv) {
				continue
			}
			switch fv.Kind() {
			case reflect.String, reflect.Bool,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
				reflect.Float32, reflect.Float64:
				parts = append(parts, fmt.Sprint(fv.Interface()))
			}
		}
		return truncate(strings.Join(parts, "/"), 120)
	default:
		return truncate(fmt.Sprint(v.Interface()), 120)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// ---- value-dependent detectors ----

// effectiveRestart reproduces the precedence used by
// dockerComposeToKomposeMapping: deploy.restart_policy.condition, when present,
// overrides the top-level restart key before the unless-stopped rewrite.
func effectiveRestart(svc types.ServiceConfig) string {
	if svc.Deploy != nil && svc.Deploy.RestartPolicy != nil && svc.Deploy.RestartPolicy.Condition != "" {
		return svc.Deploy.RestartPolicy.Condition
	}
	return svc.Restart
}

func detectUnlessStoppedRestart(svc types.ServiceConfig) (string, bool) {
	if effectiveRestart(svc) == "unless-stopped" {
		return "unless-stopped", true
	}
	return "", false
}

func detectEndpointMode(svc types.ServiceConfig) (string, bool) {
	if svc.Deploy != nil && svc.Deploy.EndpointMode != "" {
		return svc.Deploy.EndpointMode, true
	}
	return "", false
}

func detectRollbackConfig(svc types.ServiceConfig) (string, bool) {
	if svc.Deploy != nil && svc.Deploy.RollbackConfig != nil {
		return "rollback_config", true
	}
	return "", false
}

// detectUnsupportedLinks reports the key only when a link uses
// SERVICE:ALIAS with different names, which kompose cannot express.
func detectUnsupportedLinks(svc types.ServiceConfig) (string, bool) {
	for _, link := range svc.Links {
		parts := strings.Split(link, ":")
		if len(parts) == 2 && parts[0] != parts[1] {
			return strings.Join(svc.Links, ","), true
		}
	}
	return "", false
}

// detectUnsupportedNetworkMode reports any network_mode other than
// service:<name>, which fixNetworkModeToService converts.
func detectUnsupportedNetworkMode(svc types.ServiceConfig) (string, bool) {
	if svc.NetworkMode == "" || strings.HasPrefix(svc.NetworkMode, "service:") {
		return "", false
	}
	return svc.NetworkMode, true
}

// detectExplicitDependsOn avoids reporting depends_on entries that compose-go
// derives automatically from links, network_mode or volumes_from. Without this
// filter a supported `links: [db]` would also produce a depends_on diagnostic.
func detectExplicitDependsOn(svc types.ServiceConfig) (string, bool) {
	if len(svc.DependsOn) == 0 {
		return "", false
	}
	derived := map[string]struct{}{}
	for _, link := range svc.Links {
		derived[strings.Split(link, ":")[0]] = struct{}{}
	}
	if name, ok := strings.CutPrefix(svc.NetworkMode, "service:"); ok {
		derived[name] = struct{}{}
	}
	for _, from := range svc.VolumesFrom {
		derived[strings.Split(from, ":")[0]] = struct{}{}
	}
	var explicit []string
	for dep := range svc.DependsOn {
		if _, ok := derived[dep]; !ok {
			explicit = append(explicit, dep)
		}
	}
	if len(explicit) == 0 {
		return "", false
	}
	sort.Strings(explicit)
	return strings.Join(explicit, ","), true
}

// ---- root-level objects ----

// komposeVolumeLabels are the volume labels kompose actually reads.
var komposeVolumeLabels = map[string]struct{}{
	"kompose.volume.size":     {},
	"kompose.volume.selector": {},
}

func collectRootDiagnostics(project *types.Project, report *diagnostic.Report) {
	for _, name := range sortedKeys(project.Volumes) {
		vol := project.Volumes[name]
		key := "volumes." + name
		addIfSet(report, key+".driver", vol.Driver, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".driver_opts", vol.DriverOpts, "key is not supported by Kubernetes and is ignored")
		if bool(vol.External) {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key + ".external", Original: "true",
				Message: "external volumes are not converted: kompose has no way to reference a pre-existing PersistentVolume"})
		}
		if extra := nonKomposeLabels(vol.Labels); extra != "" {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key + ".labels", Original: extra,
				Message: "only kompose.volume.size and kompose.volume.selector labels are supported"})
		}
	}

	for _, name := range sortedKeys(project.Networks) {
		net := project.Networks[name]
		key := "networks." + name
		addIfSet(report, key+".driver", net.Driver, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".driver_opts", net.DriverOpts, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".ipam", net.Ipam, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".labels", net.Labels, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".enable_ipv4", net.EnableIPv4, "key is not supported by Kubernetes and is ignored")
		addIfSet(report, key+".enable_ipv6", net.EnableIPv6, "key is not supported by Kubernetes and is ignored")
		if net.Internal {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key + ".internal", Original: "true", Message: ignoredMessage})
		}
		if net.Attachable {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key + ".attachable", Original: "true", Message: ignoredMessage})
		}
		if bool(net.External) {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key + ".external", Original: "true", Message: ignoredMessage})
		}
	}

	for _, name := range sortedKeys(project.Secrets) {
		secret := project.Secrets[name]
		// External secrets are reported by the Kubernetes transformer when it
		// skips them (CreateSecrets), so they are not reported here. That keeps
		// the legacy "External secrets ... is not currently supported" phrase
		// and avoids a duplicate line for the same key.
		addIfSet(report, "secrets."+name+".driver", secret.Driver, ignoredMessage)
		addIfSet(report, "secrets."+name+".driver_opts", secret.DriverOpts, ignoredMessage)
		addIfSet(report, "secrets."+name+".template_driver", secret.TemplateDriver, ignoredMessage)
		addIfSet(report, "secrets."+name+".labels", secret.Labels, ignoredMessage)
	}

	for _, name := range sortedKeys(project.Configs) {
		config := project.Configs[name]
		if bool(config.External) {
			report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: "configs." + name + ".external", Original: "true",
				Message: "external configs are not converted - ignoring"})
		}
		addIfSet(report, "configs."+name+".driver", config.Driver, ignoredMessage)
		addIfSet(report, "configs."+name+".driver_opts", config.DriverOpts, ignoredMessage)
		addIfSet(report, "configs."+name+".template_driver", config.TemplateDriver, ignoredMessage)
		addIfSet(report, "configs."+name+".labels", config.Labels, ignoredMessage)
	}
}

func addIfSet(report *diagnostic.Report, key string, value interface{}, message string) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || isZeroReflect(v) {
		return
	}
	report.Add(diagnostic.Diagnostic{Category: diagnostic.Ignored, Key: key,
		Original: summarizeReflect(v), Message: message})
}

func nonKomposeLabels(labels types.Labels) string {
	var extra []string
	for key := range labels {
		if _, ok := komposeVolumeLabels[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return strings.Join(extra, ",")
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
