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

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes/kompose/pkg/kobject"
	log "github.com/sirupsen/logrus"
)

// captureWarnings installs a logrus hook on the standard logger and returns the
// diagnostic lines that were emitted while the returned restore function has
// not been called. Tests never read stderr; everything happens in-process.
func captureWarnings(t *testing.T) (*[]string, func()) {
	t.Helper()
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	logger.SetLevel(log.InfoLevel)

	var lines []string
	logger.AddHook(collectHook{lines: &lines})
	return &lines, func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	}
}

type collectHook struct {
	lines *[]string
}

func (h collectHook) Levels() []log.Level { return log.AllLevels }

func (h collectHook) Fire(entry *log.Entry) error {
	if strings.HasPrefix(entry.Message, "[kompose] ") {
		*h.lines = append(*h.lines, entry.Message)
	}
	return nil
}

func writeCompose(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

const composeWithIgnoredKey = `services:
  web:
    image: busybox
    ulimits:
      nproc: 65535
`

const composeFullySupported = `services:
  web:
    image: busybox
    ports:
      - "8080:80"
`

// TestConvertEmitsDiagnosticsWithoutChangingResult asserts the conversion
// result is produced and diagnostics are logged when strict mode is off.
func TestConvertEmitsDiagnosticsWithoutChangingResult(t *testing.T) {
	lines, restore := captureWarnings(t)
	defer restore()

	file := writeCompose(t, composeWithIgnoredKey)
	objects, err := Convert(kobject.ConvertOptions{
		InputFiles: []string{file},
		ToStdout:   true,
		Provider:   DefaultProvider,
	})
	if err != nil {
		t.Fatalf("conversion must still succeed without --strict: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("expected converted objects")
	}
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "category=ignored") || !strings.Contains(joined, "key=ulimits") {
		t.Fatalf("expected an ignored ulimits diagnostic, got: %v", *lines)
	}
}

// TestConvertExternalSecretKeepsLegacyPhrase asserts the transformer-collected
// external-secret degradation still contains the legacy phrase and is reported
// exactly once, through the shared diagnostic format.
func TestConvertExternalSecretKeepsLegacyPhrase(t *testing.T) {
	lines, restore := captureWarnings(t)
	defer restore()

	file := writeCompose(t, `services:
  web:
    image: busybox
    secrets:
      - ext
secrets:
  ext:
    external: true
`)
	if _, err := Convert(kobject.ConvertOptions{
		InputFiles: []string{file},
		ToStdout:   true,
		Provider:   DefaultProvider,
	}); err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	matches := 0
	for _, line := range *lines {
		if strings.Contains(line, "key=secrets.ext.external") {
			matches++
			if !strings.Contains(line, "External secrets ext is not currently supported - ignoring") {
				t.Errorf("legacy phrase must be preserved, got %q", line)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one external-secret line, got %d: %v", matches, *lines)
	}
}

// TestConvertStrictFailsOnIgnoredKey asserts --strict turns an ignored key into
// a non-zero exit while keeping the diagnostic output.
func TestConvertStrictFailsOnIgnoredKey(t *testing.T) {
	lines, restore := captureWarnings(t)
	defer restore()

	file := writeCompose(t, composeWithIgnoredKey)
	_, err := Convert(kobject.ConvertOptions{
		InputFiles: []string{file},
		ToStdout:   true,
		Provider:   DefaultProvider,
		Strict:     true,
	})
	if err == nil {
		t.Fatal("expected --strict to fail when an ignored key is present")
	}
	if !strings.Contains(err.Error(), "strict mode") {
		t.Errorf("unexpected strict error: %v", err)
	}
	if !strings.Contains(strings.Join(*lines, "\n"), "key=ulimits") {
		t.Errorf("strict mode must still emit the diagnostic, got: %v", *lines)
	}
}

// TestConvertStrictPassesOnSupportedInput asserts --strict does not fail on
// fully converted input.
func TestConvertStrictPassesOnSupportedInput(t *testing.T) {
	_, restore := captureWarnings(t)
	defer restore()

	file := writeCompose(t, composeFullySupported)
	if _, err := Convert(kobject.ConvertOptions{
		InputFiles: []string{file},
		ToStdout:   true,
		Provider:   DefaultProvider,
		Strict:     true,
	}); err != nil {
		t.Fatalf("--strict must not fail on fully supported input: %v", err)
	}
}

// TestConvertDiagnosticsDeterministicAcrossRuns asserts byte-identical output
// across repeated in-process runs.
func TestConvertDiagnosticsDeterministicAcrossRuns(t *testing.T) {
	file := writeCompose(t, `services:
  zeta:
    image: busybox
    ulimits:
      nproc: 1
  alpha:
    image: busybox
    sysctls:
      kernel.shm_rmid_forced: "1"
`)
	var first []string
	for run := 0; run < 3; run++ {
		lines, restore := captureWarnings(t)
		if _, err := Convert(kobject.ConvertOptions{
			InputFiles: []string{file},
			ToStdout:   true,
			Provider:   DefaultProvider,
		}); err != nil {
			t.Fatalf("run %d failed: %v", run, err)
		}
		current := append([]string(nil), *lines...)
		restore()
		if run == 0 {
			first = current
			continue
		}
		if strings.Join(first, "\n") != strings.Join(current, "\n") {
			t.Fatalf("diagnostics changed between runs:\nfirst: %v\nrun %d: %v", first, run, current)
		}
	}
	if len(first) == 0 {
		t.Fatal("expected diagnostics for the deterministic sample")
	}
}
