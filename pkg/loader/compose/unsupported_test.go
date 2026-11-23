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
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kubernetes/kompose/pkg/diagnostic"
	"github.com/kubernetes/kompose/pkg/kobject"
)

// loadProjectFromString is a test helper that parses a Compose document the
// same way LoadFile does.
func loadProjectFromString(t *testing.T, document string) *types.Project {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	options, err := cli.NewProjectOptions(
		[]string{file}, cli.WithOsEnv,
		cli.WithWorkingDirectory(dir),
		cli.WithInterpolation(false),
		cli.WithEnvFiles([]string{}...),
		cli.WithDotEnv,
	)
	if err != nil {
		t.Fatalf("Unable to create compose options: %v", err)
	}
	project, err := cli.ProjectFromOptions(context.Background(), options)
	if err != nil {
		t.Fatalf("Unable to load project: %v", err)
	}
	return project
}

// minimalServiceDocument returns a single-service Compose document that sets
// exactly one key. It is used by the registry invariant test.
func minimalServiceDocument(key string) string {
	const prefix = "services:\n  app:\n    image: busybox\n"
	bodies := map[string]string{
		"annotations":            "    annotations:\n      foo: bar\n",
		"attach":                 "    attach: false\n",
		"blkio_config":           "    blkio_config:\n      weight: 300\n",
		"cgroup":                 "    cgroup: host\n",
		"cgroup_parent":          "    cgroup_parent: m-executor\n",
		"cpu_count":              "    cpu_count: 2\n",
		"cpu_percent":            "    cpu_percent: 50\n",
		"cpu_period":             "    cpu_period: 100000\n",
		"cpu_quota":              "    cpu_quota: 50000\n",
		"cpu_rt_period":          "    cpu_rt_period: 1000\n",
		"cpu_rt_runtime":         "    cpu_rt_runtime: 1000\n",
		"cpu_shares":             "    cpu_shares: 512\n",
		"cpus":                   "    cpus: 2\n",
		"cpuset":                 "    cpuset: \"0,1\"\n",
		"credential_spec":        "    credential_spec:\n      file: cred.json\n",
		"depends_on":             "    depends_on:\n      - db\n  db:\n    image: busybox\n",
		"develop":                "    develop:\n      watch: []\n",
		"device_cgroup_rules":    "    device_cgroup_rules:\n      - \"c 1:3 mr\"\n",
		"devices":                "    devices:\n      - \"/dev/ttyUSB0:/dev/ttyUSB0\"\n",
		"dns":                    "    dns: 8.8.8.8\n",
		"dns_opt":                "    dns_opt:\n      - ndots:1\n",
		"dns_search":             "    dns_search: example.com\n",
		"deploy.endpoint_mode":   "    deploy:\n      endpoint_mode: vip\n",
		"deploy.rollback_config": "    deploy:\n      rollback_config:\n        parallelism: 1\n",
		"external_links":         "    external_links:\n      - db\n",
		"extra_hosts":            "    extra_hosts:\n      - \"somehost=162.242.195.82\"\n",
		"gpus":                   "    gpus: all\n",
		"init":                   "    init: true\n",
		"ipc":                    "    ipc: host\n",
		"isolation":              "    isolation: default\n",
		"links":                  "    links:\n      - \"db:alias\"\n  db:\n    image: busybox\n",
		"logging":                "    logging:\n      driver: syslog\n",
		"mac_address":            "    mac_address: 02:42:ac:11:65:43\n",
		"mem_reservation":        "    mem_reservation: 1M\n",
		"mem_swappiness":         "    mem_swappiness: 60\n",
		"memswap_limit":          "    memswap_limit: 1G\n",
		"models":                 "    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n",
		"network_mode":           "    network_mode: host\n",
		"oom_kill_disable":       "    oom_kill_disable: true\n",
		"oom_score_adj":          "    oom_score_adj: 500\n",
		"pids_limit":             "    pids_limit: 100\n",
		"platform":               "    platform: linux/amd64\n",
		"post_start":             "    post_start:\n      - command: echo hi\n",
		"pre_stop":               "    pre_stop:\n      - command: echo bye\n",
		"provider":               "    provider:\n      type: foo\n",
		"pull_policy":            "    pull_policy: always\n",
		"restart":                "    restart: unless-stopped\n",
		"runtime":                "    runtime: runc\n",
		"scale":                  "    scale: 3\n",
		"security_opt":           "    security_opt:\n      - label=disable\n",
		"shm_size":               "    shm_size: 64M\n",
		"storage_opt":            "    storage_opt:\n      size: 1G\n",
		"stop_signal":            "    stop_signal: SIGUSR1\n",
		"sysctls":                "    sysctls:\n      net.core.somaxconn: 1024\n",
		"ulimits":                "    ulimits:\n      nproc: 65535\n",
		"userns_mode":            "    userns_mode: host\n",
		"use_api_socket":         "    use_api_socket: true\n",
		"uts":                    "    uts: host\n",
		"volumes_from":           "    volumes_from:\n      - db\n  db:\n    image: busybox\n",
	}
	return prefix + bodies[key]
}

// TestKeyRegistryMatchesImplementation is the guard against list drift.
//
// For every entry in the registry it builds a minimal Compose input containing
// only that key and asserts that a diagnostic is produced. It also asserts the
// referenced compose-go field still exists, so a compose-go rename (for
// example EnvFile -> EnvFiles) fails loudly instead of silently disabling
// detection.
func TestKeyRegistryMatchesImplementation(t *testing.T) {
	serviceType := reflect.TypeOf(types.ServiceConfig{})
	for _, spec := range composeServiceKeySpecs {
		spec := spec
		t.Run(spec.YAMLKey, func(t *testing.T) {
			if _, ok := serviceType.FieldByName(spec.GoField); !ok {
				t.Fatalf("registry key %q references compose-go field %q which no longer exists", spec.YAMLKey, spec.GoField)
			}
			document := minimalServiceDocument(spec.YAMLKey)
			// For nested keys such as deploy.endpoint_mode the document uses
			// the leaf yaml name, so check the leaf rather than the dotted path.
			leaf := spec.YAMLKey
			if idx := strings.LastIndex(leaf, "."); idx >= 0 {
				leaf = leaf[idx+1:]
			}
			if !strings.Contains(document, leaf+":") {
				t.Fatalf("test input for %q does not actually contain the key", spec.YAMLKey)
			}
			project := loadProjectFromString(t, document)
			report := checkUnsupportedKey(project)
			var found *diagnostic.Item
			for _, item := range report.Items() {
				if item.Key == spec.YAMLKey && item.Category == spec.Category {
					item := item
					found = &item
					break
				}
			}
			if found == nil {
				t.Fatalf("key %q produced no %q diagnostic; list and implementation disagree. Lines: %v",
					spec.YAMLKey, spec.Category, report.Lines())
			}
		})
	}
}

// TestRegistryDoesNotContainSupportedKeys guards the other direction: keys that
// kompose really converts must not be reported. It encodes the corrections for
// read_only and env_file (compose-go v2 calls the field EnvFiles), which the
// old hard-coded list got wrong.
func TestRegistryDoesNotContainSupportedKeys(t *testing.T) {
	forbidden := map[string]bool{
		"read_only":   true,
		"env_file":    true,
		"environment": true,
		"ports":       true,
		"volumes":     true,
		"image":       true,
	}
	for _, spec := range composeServiceKeySpecs {
		if forbidden[spec.YAMLKey] {
			t.Errorf("registry still lists %q, but conversion supports it", spec.YAMLKey)
		}
		if spec.GoField == "EnvFile" {
			t.Errorf("registry uses compose-go v1 field name EnvFile; v2 uses EnvFiles")
		}
	}
	if _, ok := reflect.TypeOf(types.ServiceConfig{}).FieldByName("EnvFiles"); !ok {
		t.Errorf("expected compose-go ServiceConfig to expose EnvFiles")
	}
}

// TestSupportedKeysProduceNoDiagnostics verifies a handful of keys that are
// fully converted produce no degradation report.
func TestSupportedKeysProduceNoDiagnostics(t *testing.T) {
	document := `services:
  app:
    image: busybox
    read_only: true
    restart: always
    ports:
      - "8080:80"
    deploy:
      replicas: 2
    network_mode: "service:db"
    links:
      - db
  db:
    image: busybox
`
	report := checkUnsupportedKey(loadProjectFromString(t, document))
	if lines := report.Lines(); len(lines) != 0 {
		t.Fatalf("expected no diagnostics for fully converted keys, got: %v", lines)
	}
}

// TestZeroValueExplicitFieldsDoNotTrigger covers privileged: false, ulimits: {}
// and sysctls: {}. Empty maps must count as absent, matching empty slices.
func TestZeroValueExplicitFieldsDoNotTrigger(t *testing.T) {
	document := `services:
  app:
    image: busybox
    privileged: false
    ulimits: {}
    sysctls: {}
    devices: []
`
	report := checkUnsupportedKey(loadProjectFromString(t, document))
	if lines := report.Lines(); len(lines) != 0 {
		t.Fatalf("explicit zero values must not trigger diagnostics, got: %v", lines)
	}
}

// TestRestartOverrideByDeployPolicy ensures the registry follows the same
// precedence as the mapping: deploy.restart_policy.condition overrides the
// top-level restart key, so an overridden unless-stopped is not reported.
func TestRestartOverrideByDeployPolicy(t *testing.T) {
	document := `services:
  app:
    image: busybox
    restart: unless-stopped
    deploy:
      restart_policy:
        condition: on-failure
`
	lines := checkUnsupportedKey(loadProjectFromString(t, document)).Lines()
	if len(lines) != 0 {
		t.Fatalf("overridden unless-stopped must not be reported, got: %v", lines)
	}
}

// TestEndpointModeCategoryDependsOnValue locks the split: vip changes the
// Service type (approximated), dnsrr leaves no trace (ignored).
func TestEndpointModeCategoryDependsOnValue(t *testing.T) {
	vip := `services:
  app:
    image: busybox
    deploy:
      endpoint_mode: vip
`
	dnsrr := `services:
  app:
    image: busybox
    deploy:
      endpoint_mode: dnsrr
`
	vipItems := checkUnsupportedKey(loadProjectFromString(t, vip)).Items()
	if len(vipItems) != 1 || vipItems[0].Category != diagnostic.Approximated || vipItems[0].Converted != "NodePort" {
		t.Fatalf("vip must be approximated to NodePort, got %+v", vipItems)
	}
	dnsrrItems := checkUnsupportedKey(loadProjectFromString(t, dnsrr)).Items()
	if len(dnsrrItems) != 1 || dnsrrItems[0].Category != diagnostic.Ignored {
		t.Fatalf("dnsrr must be ignored, got %+v", dnsrrItems)
	}
}

// TestExtensionFieldsIgnored confirms x-* fields are part of the Compose spec
// and never produce a diagnostic.
func TestExtensionFieldsIgnored(t *testing.T) {
	document := `x-shared: &shared
  image: busybox
services:
  app:
    <<: *shared
    x-service-foo: bar
`
	report := checkUnsupportedKey(loadProjectFromString(t, document))
	if lines := report.Lines(); len(lines) != 0 {
		t.Fatalf("extension fields must not be reported, got: %v", lines)
	}
}

// TestLinksConditionalSupport covers both supported and unsupported link
// forms.
func TestLinksConditionalSupport(t *testing.T) {
	supported := `services:
  app:
    image: busybox
    links:
      - db
      - "db:db"
  db:
    image: busybox
`
	if lines := checkUnsupportedKey(loadProjectFromString(t, supported)).Lines(); len(lines) != 0 {
		t.Fatalf("links with identical alias must not be reported, got: %v", lines)
	}

	unsupported := `services:
  app:
    image: busybox
    links:
      - "db:alias"
  db:
    image: busybox
`
	items := checkUnsupportedKey(loadProjectFromString(t, unsupported)).Items()
	var linkItem *diagnostic.Item
	for _, item := range items {
		if item.Key == "links" {
			item := item
			linkItem = &item
		}
	}
	if linkItem == nil {
		t.Fatalf("links with a distinct alias must be reported, got: %v", items)
	}
	if linkItem.Category != diagnostic.Conditional {
		t.Errorf("links must be conditional, got %q", linkItem.Category)
	}
	if linkItem.Condition == "" {
		t.Errorf("conditional diagnostics must carry a condition")
	}
}

// TestAggregationAndTruncation checks that the same key in many services is
// aggregated into one line, sorted, and truncated deterministically.
func TestAggregationAndTruncation(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n")
	const total = 12
	for i := 0; i < total; i++ {
		b.WriteString("  svc")
		// Intentionally reverse-pad to prove output sorting is independent of
		// declaration order.
		b.WriteString(string(rune('A' + (total - 1 - i))))
		b.WriteString(":\n    image: busybox\n    ulimits:\n      nproc: 65535\n")
	}
	report := checkUnsupportedKey(loadProjectFromString(t, b.String()))

	var ulimit *diagnostic.Item
	count := 0
	for _, item := range report.Items() {
		if item.Key == "ulimits" {
			item := item
			ulimit = &item
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one aggregated ulimits diagnostic, got %d", count)
	}
	if ulimit.ServiceCount != total {
		t.Errorf("expected service count %d, got %d", total, ulimit.ServiceCount)
	}
	if len(ulimit.Services) != diagnostic.MaxServicesPerLine {
		t.Errorf("expected %d rendered services, got %d", diagnostic.MaxServicesPerLine, len(ulimit.Services))
	}
	if !ulimit.Truncated {
		t.Errorf("expected truncated services list")
	}
	if !sort.StringsAreSorted(ulimit.Services) {
		t.Errorf("services must be sorted, got %v", ulimit.Services)
	}
	line := ulimit.Line()
	if !strings.Contains(line, "total=12") {
		t.Errorf("truncated line must state the total, got %q", line)
	}
}

// TestDiagnosticsAreDeterministic ensures repeated runs over the same input
// produce byte-identical lines.
func TestDiagnosticsAreDeterministic(t *testing.T) {
	document := `services:
  zeta:
    image: busybox
    ulimits:
      nproc: 1
    sysctls:
      kernel.shm_rmid_forced: "1"
  alpha:
    image: busybox
    ulimits:
      nproc: 2
    devices:
      - "/dev/a:/dev/a"
`
	project := loadProjectFromString(t, document)
	first := checkUnsupportedKey(project).Lines()
	for i := 0; i < 5; i++ {
		next := checkUnsupportedKey(project).Lines()
		if !reflect.DeepEqual(first, next) {
			t.Fatalf("diagnostic output changed between runs:\nfirst: %v\nnext:  %v", first, next)
		}
	}
}

// TestRootLevelDiagnostics covers root-level volumes, networks, secrets and
// configs.
func TestRootLevelDiagnostics(t *testing.T) {
	document := `services:
  app:
    image: busybox
volumes:
  data:
    driver: local
    driver_opts:
      type: nfs
  ext:
    external: true
networks:
  backend:
    driver: bridge
    internal: true
secrets:
  ext:
    external: true
configs:
  ext:
    external: true
`
	lines := strings.Join(checkUnsupportedKey(loadProjectFromString(t, document)).Lines(), "\n")
	for _, key := range []string{
		"volumes.data.driver",
		"volumes.data.driver_opts",
		"volumes.ext.external",
		"networks.backend.driver",
		"networks.backend.internal",
		"configs.ext.external",
	} {
		if !strings.Contains(lines, "key="+key) {
			t.Errorf("expected diagnostic for %q, got:\n%s", key, lines)
		}
	}
	// External secrets are reported at conversion time by the Kubernetes
	// transformer, not by the loader-only collector, so they must not appear
	// twice here.
	if strings.Contains(lines, "secrets.ext.external") {
		t.Errorf("external secrets must not be reported by the loader collector, got:\n%s", lines)
	}
}

// TestRejectedDiagnosticForInvalidLabel covers the Rejected/InvalidInput
// category using an existing rejection that returns an error.
func TestRejectedDiagnosticForInvalidLabel(t *testing.T) {
	report := diagnostic.NewReport()
	serviceConfig := &kobject.ServiceConfig{Name: "app"}
	err := parseKomposeLabelsWithReport(map[string]string{
		LabelNodePortPort: "30080",
	}, serviceConfig, report)
	if err == nil {
		t.Fatal("expected parseKomposeLabelsWithReport to reject a nodeport port without nodeport type")
	}
	items := report.Items()
	if len(items) != 1 {
		t.Fatalf("expected one diagnostic, got %v", items)
	}
	if items[0].Category != diagnostic.Rejected {
		t.Errorf("expected Rejected category, got %q", items[0].Category)
	}
	if items[0].Origin != diagnostic.OriginInvalidInput {
		t.Errorf("expected invalid-input origin, got %q", items[0].Origin)
	}
	if items[0].Location == "" {
		t.Errorf("rejected diagnostics must carry a location")
	}
}
