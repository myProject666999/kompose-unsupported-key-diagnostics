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

// Package diagnostic defines the structured degradation diagnostics emitted by
// kompose while converting a Compose file.
//
// A diagnostic answers four questions: what kind of degradation happened, in
// which service/key, with which original value, and why. The same type is used
// by the compose loader and by both transformers so that every degradation can
// be rendered through a single, stable log format.
package diagnostic

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// Category classifies how a Compose key degraded during conversion.
type Category string

const (
	// Ignored means the key leaves no trace at all in the converted output.
	Ignored Category = "ignored"
	// Approximated means the key is converted, but the semantics differ from
	// Compose (for example restart: unless-stopped becomes always).
	Approximated Category = "approximated"
	// Conditional means the key only takes effect for specific values (for
	// example network_mode only supports the "service:<name>" form).
	Conditional Category = "conditional"
	// Rejected means conversion cannot continue once the key is seen.
	Rejected Category = "rejected"
)

// Origin is only meaningful for Rejected diagnostics. It separates invalid
// user input (the Compose file itself must change) from valid input that
// kompose does not support (the user needs a label or another workaround).
type Origin string

const (
	// OriginInvalidInput means the Compose file is internally invalid or
	// contradictory, for example a NodePort port without a NodePort service.
	OriginInvalidInput Origin = "invalid-input"
	// OriginUnsupported means the Compose file is valid but kompose has no
	// conversion for it.
	OriginUnsupported Origin = "unsupported"
)

// Diagnostic is one structured degradation report.
type Diagnostic struct {
	// Category is the degradation class.
	Category Category
	// Service is the Compose service the key belongs to. It is empty for
	// root-level objects (volumes, networks, secrets), whose name is instead
	// embedded in Key.
	Service string
	// Key is the Compose key path, for example "ulimits",
	// "deploy.endpoint_mode" or "volumes.data.driver".
	Key string
	// Original is a short, deterministic summary of the user-provided value.
	Original string
	// Converted is the value produced by kompose. It is only set for
	// Approximated diagnostics.
	Converted string
	// Condition describes when a Conditional key is actually supported.
	Condition string
	// Origin separates invalid input from unsupported-but-valid input.
	Origin Origin
	// Location is the code location that rejected the input, used for
	// Rejected diagnostics.
	Location string
	// Message is the human readable explanation. It intentionally keeps the
	// key phrases of the legacy warning so existing CI grep patterns keep
	// matching.
	Message string
}

// maxServicesPerLine bounds the number of service names rendered on one line.
// Services are sorted first, so the truncation rule is deterministic.
const maxServicesPerLine = 10

// MaxServicesPerLine is the exported bound, for tests and documentation.
const MaxServicesPerLine = maxServicesPerLine

type groupKey struct {
	category Category
	key      string
	message  string
}

type group struct {
	category   Category
	key        string
	message    string
	origin     Origin
	location   string
	services   map[string]struct{}
	originals  map[string]struct{}
	converteds map[string]struct{}
	conditions map[string]struct{}
}

// Report aggregates diagnostics by (category, key, message).
//
// Aggregation keeps a Compose key used by many services on a single line and
// keeps the output byte-for-byte deterministic: services and values are sorted
// before rendering and Go map iteration order never leaks into the log.
type Report struct {
	mu     sync.Mutex
	groups map[groupKey]*group
}

// NewReport returns an empty report.
func NewReport() *Report {
	return &Report{groups: map[groupKey]*group{}}
}

// Add records one diagnostic. It is safe to call on a nil report so callers
// that construct objects by hand do not need nil checks.
func (r *Report) Add(d Diagnostic) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	k := groupKey{category: d.Category, key: d.Key, message: d.Message}
	g, ok := r.groups[k]
	if !ok {
		g = &group{
			category:   d.Category,
			key:        d.Key,
			message:    d.Message,
			services:   map[string]struct{}{},
			originals:  map[string]struct{}{},
			converteds: map[string]struct{}{},
			conditions: map[string]struct{}{},
		}
		r.groups[k] = g
	}
	if d.Service != "" {
		g.services[d.Service] = struct{}{}
	}
	if d.Original != "" {
		g.originals[d.Original] = struct{}{}
	}
	if d.Converted != "" {
		g.converteds[d.Converted] = struct{}{}
	}
	if d.Condition != "" {
		g.conditions[d.Condition] = struct{}{}
	}
	if g.origin == "" && d.Origin != "" {
		g.origin = d.Origin
	}
	if g.location == "" && d.Location != "" {
		g.location = d.Location
	}
}

// Item is an aggregated diagnostic ready for rendering.
type Item struct {
	Diagnostic
	// Services holds the sorted service names, capped at maxServicesPerLine.
	Services []string
	// ServiceCount is the total number of distinct services.
	ServiceCount int
	// Truncated reports whether Services was capped.
	Truncated bool
}

// Items returns the aggregated diagnostics in a deterministic order.
func (r *Report) Items() []Item {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	items := make([]Item, 0, len(r.groups))
	for _, g := range r.groups {
		items = append(items, g.item())
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return categoryRank(items[i].Category) < categoryRank(items[j].Category)
		}
		if items[i].Key != items[j].Key {
			return items[i].Key < items[j].Key
		}
		return items[i].Message < items[j].Message
	})
	return items
}

func (g *group) item() Item {
	it := Item{
		Diagnostic: Diagnostic{
			Category:  g.category,
			Key:       g.key,
			Original:  joinSorted(g.originals),
			Converted: joinSorted(g.converteds),
			Condition: joinSorted(g.conditions),
			Origin:    g.origin,
			Location:  g.location,
			Message:   g.message,
		},
		ServiceCount: len(g.services),
	}
	all := make([]string, 0, len(g.services))
	for name := range g.services {
		all = append(all, name)
	}
	sort.Strings(all)
	if len(all) > maxServicesPerLine {
		it.Truncated = true
		all = all[:maxServicesPerLine]
	}
	it.Services = all
	if len(it.Services) == 1 {
		it.Service = it.Services[0]
	}
	return it
}

func joinSorted(set map[string]struct{}) string {
	if len(set) == 0 {
		return ""
	}
	values := make([]string, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func categoryRank(c Category) int {
	switch c {
	case Ignored:
		return 0
	case Approximated:
		return 1
	case Conditional:
		return 2
	case Rejected:
		return 3
	default:
		return 4
	}
}

// Line renders an item as one stable, greppable log line.
//
// The format is fixed: category, key, services, original, converted,
// condition, origin, location, message, always in that order and always with
// the same quoting. Optional fields are omitted entirely when empty.
func (i Item) Line() string {
	var b strings.Builder
	b.WriteString("[kompose] category=")
	b.WriteString(string(i.Category))
	b.WriteString(" key=")
	b.WriteString(i.Key)
	if len(i.Services) > 0 {
		b.WriteString(" services=")
		b.WriteString(strings.Join(i.Services, ","))
		if i.Truncated {
			b.WriteString(" (+")
			b.WriteString(strconv.Itoa(i.ServiceCount - len(i.Services)))
			b.WriteString(" more, total=")
			b.WriteString(strconv.Itoa(i.ServiceCount))
			b.WriteString(")")
		}
	}
	if i.Original != "" {
		b.WriteString(" original=")
		b.WriteString(strconv.Quote(i.Original))
	}
	if i.Converted != "" {
		b.WriteString(" converted=")
		b.WriteString(strconv.Quote(i.Converted))
	}
	if i.Condition != "" {
		b.WriteString(" condition=")
		b.WriteString(strconv.Quote(i.Condition))
	}
	if i.Origin != "" {
		b.WriteString(" origin=")
		b.WriteString(string(i.Origin))
	}
	if i.Location != "" {
		b.WriteString(" location=")
		b.WriteString(i.Location)
	}
	b.WriteString(" message=")
	b.WriteString(strconv.Quote(i.Message))
	return b.String()
}

// Lines renders the whole report.
func (r *Report) Lines() []string {
	items := r.Items()
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, it.Line())
	}
	return lines
}

// Emit renders the report to logrus. Ignored, approximated and conditional
// diagnostics are warnings; rejected diagnostics are errors so that
// --suppress-warnings (which raises the level to Error) can never hide them.
func (r *Report) Emit() {
	if r == nil {
		return
	}
	for _, it := range r.Items() {
		line := it.Line()
		if it.Category == Rejected {
			log.Error(line)
		} else {
			log.Warn(line)
		}
	}
}

// HasStrictViolations reports whether strict mode should fail. Strict mode
// treats ignored and approximated keys as errors; conditional keys are still
// supported for the documented values and rejected keys already abort.
func (r *Report) HasStrictViolations() bool {
	return len(r.StrictViolations()) > 0
}

// StrictViolations returns the ignored and approximated items.
func (r *Report) StrictViolations() []Item {
	var violations []Item
	for _, it := range r.Items() {
		if it.Category == Ignored || it.Category == Approximated {
			violations = append(violations, it)
		}
	}
	return violations
}
