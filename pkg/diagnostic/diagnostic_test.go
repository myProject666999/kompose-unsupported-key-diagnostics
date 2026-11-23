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

package diagnostic

import (
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

// captureHook collects emitted log entries for in-process assertions. Tests use
// an injected logrus hook instead of reading stderr, so no output parsing and
// no subprocess is needed.
type captureHook struct {
	entries *[]log.Entry
}

func (h captureHook) Levels() []log.Level { return log.AllLevels }

func (h captureHook) Fire(entry *log.Entry) error {
	*h.entries = append(*h.entries, *entry)
	return nil
}

func newCapture(t *testing.T) (*[]log.Entry, func()) {
	t.Helper()
	var entries []log.Entry
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	previousFormatter := logger.Formatter
	logger.SetLevel(log.InfoLevel)
	logger.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableColors: true})
	logger.AddHook(captureHook{entries: &entries})
	return &entries, func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
		logger.SetFormatter(previousFormatter)
	}
}

func TestReportEmitsSingleLinePerGroup(t *testing.T) {
	entries, restore := newCapture(t)
	defer restore()

	report := NewReport()
	for _, svc := range []string{"web", "api", "web"} {
		report.Add(Diagnostic{Category: Ignored, Service: svc, Key: "ulimits", Original: "nproc=1", Message: "not supported"})
	}
	report.Emit()

	if len(*entries) != 1 {
		t.Fatalf("expected one aggregated log line, got %d", len(*entries))
	}
	msg := (*entries)[0].Message
	if !strings.Contains(msg, "key=ulimits") || !strings.Contains(msg, "services=api,web") {
		t.Errorf("unexpected aggregated line: %q", msg)
	}
}

func TestReportLineFormatIsStable(t *testing.T) {
	report := NewReport()
	report.Add(Diagnostic{
		Category:  Approximated,
		Service:   "web",
		Key:       "restart",
		Original:  "unless-stopped",
		Converted: "always",
		Message:   "Restart policy 'unless-stopped' is not supported, convert it to 'always'",
	})
	report.Add(Diagnostic{
		Category:  Conditional,
		Service:   "api",
		Key:       "network_mode",
		Original:  "host",
		Condition: "network_mode must be service:<name>",
		Message:   "network_mode is only supported in the form 'service:<name>'",
	})
	report.Add(Diagnostic{
		Category: Rejected,
		Origin:   OriginInvalidInput,
		Service:  "db",
		Key:      "kompose.service.nodeport.port",
		Location: "pkg/loader/compose/compose.go:parseKomposeLabels",
		Message:  "kompose.service.type must be nodeport when assign node port value",
	})

	want := []string{
		`[kompose] category=approximated key=restart services=web original="unless-stopped" converted="always" message="Restart policy 'unless-stopped' is not supported, convert it to 'always'"`,
		`[kompose] category=conditional key=network_mode services=api original="host" condition="network_mode must be service:<name>" message="network_mode is only supported in the form 'service:<name>'"`,
		`[kompose] category=rejected key=kompose.service.nodeport.port services=db origin=invalid-input location=pkg/loader/compose/compose.go:parseKomposeLabels message="kompose.service.type must be nodeport when assign node port value"`,
	}
	got := report.Lines()
	if len(got) != len(want) {
		t.Fatalf("line count mismatch:\nwant %d\ngot  %d\n%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d mismatch:\nwant %q\ngot  %q", i, want[i], got[i])
		}
	}
}

func TestSuppressWarningsNeverHidesRejected(t *testing.T) {
	entries, restore := newCapture(t)
	defer restore()

	// --suppress-warnings raises the level to Error. Ignored diagnostics are
	// warnings and must disappear; rejected diagnostics are errors and must
	// remain.
	log.SetLevel(log.ErrorLevel)

	report := NewReport()
	report.Add(Diagnostic{Category: Ignored, Key: "ulimits", Message: "ignored"})
	report.Add(Diagnostic{Category: Rejected, Key: "ports", Message: "rejected"})
	report.Emit()

	if len(*entries) != 1 {
		t.Fatalf("expected only the rejected diagnostic at error level, got %d entries", len(*entries))
	}
	if !strings.Contains((*entries)[0].Message, "category=rejected") {
		t.Errorf("the surviving entry must be the rejected one, got %q", (*entries)[0].Message)
	}
}

func TestStrictViolations(t *testing.T) {
	report := NewReport()
	report.Add(Diagnostic{Category: Ignored, Key: "ulimits", Message: "x"})
	report.Add(Diagnostic{Category: Approximated, Key: "restart", Message: "y"})
	report.Add(Diagnostic{Category: Conditional, Key: "links", Message: "z"})
	report.Add(Diagnostic{Category: Rejected, Key: "ports", Message: "w"})

	if !report.HasStrictViolations() {
		t.Fatal("expected strict violations")
	}
	violations := report.StrictViolations()
	if len(violations) != 2 {
		t.Fatalf("strict mode must only count ignored and approximated keys, got %d", len(violations))
	}

	clean := NewReport()
	clean.Add(Diagnostic{Category: Conditional, Key: "links", Message: "z"})
	if clean.HasStrictViolations() {
		t.Error("conditional diagnostics must not fail strict mode")
	}
}

func TestNilReportIsSafe(t *testing.T) {
	var report *Report
	report.Add(Diagnostic{Category: Ignored})
	report.Emit()
	if report.Lines() != nil {
		t.Error("nil report should have no lines")
	}
	if report.HasStrictViolations() {
		t.Error("nil report should have no strict violations")
	}
}

// TestHookCompatibilityWithTestHook demonstrates the injectable-hook contract
// using logrus' own test hook, which is the mechanism app-level tests use. It
// confirms Emit writes through the standard logger and is observable without
// reading stderr.
func TestHookCompatibilityWithTestHook(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	logger.SetLevel(log.InfoLevel)
	_, hook := test.NewNullLogger()
	defer func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	}()

	logger.AddHook(hook)

	report := NewReport()
	report.Add(Diagnostic{Category: Ignored, Service: "web", Key: "sysctls", Message: "ignored"})
	report.Emit()

	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("expected the test hook to capture the emitted diagnostic")
	}
	if !strings.Contains(entry.Message, "key=sysctls") {
		t.Errorf("unexpected captured message: %q", entry.Message)
	}
}
