package httpclient

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The refusal tables are the consumer half of the fleet idempotency rule: a
// callee's RETRYABLE refusal is released under its Idempotency-Key, so this
// context retries it, and everything else is terminal. These tests keep each
// table honest against the byte-pinned copy of the callee's OpenAPI.
//
// What the pinned spec can prove, and what it cannot:
//   - it names, per idempotent operation this context calls, every problem type
//     that releases the key (the "not stored" sentence of the Idempotency-Key
//     parameter) and, in the responses and the same parameter text, the final
//     ones it documents. Both are read here: a slug the spec names that the table
//     leaves out, or a spec-released slug the table calls terminal, fails.
//   - it does NOT enumerate the callee's catalogue (the class table lives in the
//     callee's ADR 0020 / 0016 / 0020, not in the OpenAPI), so a refusal that no
//     response text names (offer-availability's pack-level-order, ...) is held by
//     the table's own entries and cannot be discovered from the pin.

type calleeSpec struct {
	callee string
	file   string
	table  refusalTable
	calls  []struct{ method, path string }
	// releaseAnchor opens the sentence of the Idempotency-Key text that lists the
	// problem types released under the key; it ends at the first "5xx".
	releaseAnchor string
}

func calleeSpecs() []calleeSpec {
	return []calleeSpec{
		{"node-topology", "node-topology.openapi.yaml", nodeTopologyRefusals,
			[]struct{ method, path string }{{http.MethodPost, "/nodes"}, {http.MethodPut, "/nodes/{nodeId}/operational-profile"}},
			"release the key:"},
		{"offer-availability", "offer-availability.openapi.yaml", offerAvailabilityRefusals,
			[]struct{ method, path string }{{http.MethodPost, "/inventory-feeds"}},
			"runs the request again:"},
		{"shipping-and-tracking", "shipping-and-tracking.openapi.yaml", shippingTrackingRefusals,
			[]struct{ method, path string }{{http.MethodPost, "/labels"}},
			"executes again, namely"},
	}
}

// notProblemTypes are hyphenated words of the spec prose that are not problem types.
var notProblemTypes = map[string]bool{
	"vendor-connectivity": true, "customer-ordering": true, "node-topology": true, "ship-to": true, "state-changing": true,
	"inventory-feeds": true, "well-formed": true, "client-generated": true, "in-org": true,
}

var slugWord = regexp.MustCompile(`(?:^|[^/A-Za-z0-9-])([a-z]+(?:-[a-z]+)+)(?:[^/A-Za-z0-9-]|$)`)

func slugsIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	// Overlapping matches (a slug directly followed by another after one separator)
	// need a second pass: scan from each match's slug end.
	for rest := text; ; {
		m := slugWord.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}
		slug := rest[m[2]:m[3]]
		if !notProblemTypes[slug] && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
		rest = rest[m[3]:]
	}
	sort.Strings(out)
	return out
}

func words(s string) string { return strings.Join(strings.Fields(s), " ") }

func idempotencyKeyText(op *openapi3.Operation) string {
	for _, p := range op.Parameters {
		if p.Value != nil && p.Value.Name == "Idempotency-Key" {
			return words(p.Value.Description)
		}
	}
	return ""
}

// releasedList is the part of the Idempotency-Key text that lists the problem types
// released under the key: from the callee's anchor phrase to the first "5xx".
func releasedList(t *testing.T, cs calleeSpec, text string) string {
	t.Helper()
	i := strings.Index(text, cs.releaseAnchor)
	if i < 0 {
		t.Fatalf("%s: the Idempotency-Key text no longer has %q: the callee reworded how it lists the released refusals; read the new text and update the anchor and the table together", cs.callee, cs.releaseAnchor)
	}
	list := text[i+len(cs.releaseAnchor):]
	if j := strings.Index(list, "5xx"); j >= 0 {
		list = list[:j]
	}
	return list
}

// responseSlugs are the problem types the 4xx responses of an operation describe.
func responseSlugs(op *openapi3.Operation, into map[string]bool) {
	for status, ref := range op.Responses.Map() {
		if !strings.HasPrefix(status, "4") || ref.Value == nil || ref.Value.Description == nil {
			continue
		}
		for _, s := range slugsIn(words(*ref.Value.Description)) {
			into[s] = true
		}
	}
}

// specSlugs reads, for every operation the callee is called on, the problem types
// the pinned spec names: released is the subset it says is not stored.
func specSlugs(t *testing.T, cs calleeSpec) (released, named []string) {
	t.Helper()
	doc := loadPinnedSpec(t, cs.file)
	rel := map[string]bool{}
	all := map[string]bool{}
	for _, c := range cs.calls {
		item := doc.Paths.Find(c.path)
		if item == nil || item.Operations()[c.method] == nil {
			t.Fatalf("%s: the pinned spec has no %s %s", cs.callee, c.method, c.path)
		}
		op := item.Operations()[c.method]
		text := idempotencyKeyText(op)
		for _, s := range slugsIn(releasedList(t, cs, text)) {
			rel[s] = true
		}
		for _, s := range slugsIn(text) {
			all[s] = true
		}
		responseSlugs(op, all)
	}
	return keys(rel), keys(all)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// auditTable returns what is wrong between a table and the spec's reading.
func auditTable(table refusalTable, released, named []string) []string {
	var problems []string
	for _, s := range released {
		if table[s] != classRetryable {
			problems = append(problems, "spec releases "+s+" under the key but the table does not map it retryable")
		}
	}
	for _, s := range named {
		if table[s] == classUnset {
			problems = append(problems, "spec names "+s+" and the table classifies it neither retryable nor terminal")
		}
	}
	return problems
}

func TestEveryProblemTypeThePinnedSpecNamesIsClassifiedAndEveryReleasedOneRetries(t *testing.T) {
	for _, cs := range calleeSpecs() {
		released, named := specSlugs(t, cs)
		if len(released) < 5 || len(named) <= len(released) {
			t.Fatalf("%s: read %d released and %d named problem types from the pin; the extraction is broken", cs.callee, len(released), len(named))
		}
		if problems := auditTable(cs.table, released, named); len(problems) > 0 {
			t.Errorf("%s:\n  %s", cs.callee, strings.Join(problems, "\n  "))
		}
	}
}

// The audit itself must fail when a table drifts: drop an entry, or flip a released
// slug to terminal, and it reports it.
func TestTheAuditFailsWhenATableDrifts(t *testing.T) {
	cs := calleeSpecs()[0]
	released, named := specSlugs(t, cs)
	dropped := refusalTable{}
	for k, v := range cs.table {
		dropped[k] = v
	}
	delete(dropped, "vendor-node-code-taken")
	dropped["unknown-region"] = classTerminal
	got := strings.Join(auditTable(dropped, released, named), "\n")
	for _, want := range []string{"vendor-node-code-taken", "unknown-region"} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit missed %s:\n%s", want, got)
		}
	}
}

// Each table entry is one of the two explicit classes; a zero class would read as
// "unknown" and be terminal by accident.
func TestEveryTableEntryIsExplicit(t *testing.T) {
	for _, cs := range calleeSpecs() {
		for slug, class := range cs.table {
			if class != classRetryable && class != classTerminal {
				t.Errorf("%s: %s has class %d", cs.callee, slug, class)
			}
		}
	}
}

// The slugs this change was briefed with are in the tables with the class of the
// callee's merged ADR table (node-topology ADR 0020, offer-availability ADR 0016,
// shipping-and-tracking ADR 0020): the spec test above cannot see every one.
func TestTheTablesCarryTheMergedADRClasses(t *testing.T) {
	retryable := map[string][]string{
		"node-topology": {"not-found", "illegal-transition", "concurrent-update", "incomplete-operational-profile", "serving-station-not-allowed",
			"lane-endpoint-not-active", "duplicate-lane", "not-a-delivery-station", "handoff-node-not-usable", "unknown-region"},
		"offer-availability": {"listing-not-on-node", "concurrent-update", "pack-level-not-found", "node-not-sellable", "not-found", "illegal-transition",
			"pack-level-order", "invalid-pack-quantity", "invalid-pack-dimensions", "validation-failed"},
		"shipping-and-tracking": {"validation-failed", "address-suspect", "pickup-point-unavailable", "destination-prefix-unavailable", "tracking-number-taken", "concurrent-update"},
	}
	terminal := map[string][]string{
		"node-topology":         {"validation-failed", "vendor-node-code-taken", "stale-profile-revision", "node-retired", "operator-not-allowed-for-node-type", "idempotency-key-reused"},
		"offer-availability":    {"negative-availability", "feed-already-applied", "duplicate-listing", "gtin-already-assigned", "idempotency-key-reused"},
		"shipping-and-tracking": {"label-already-issued", "not-found", "void-after-handover", "journey-closed", "duplicate-scan", "scan-out-of-order", "idempotency-key-reused"},
	}
	for _, cs := range calleeSpecs() {
		retry := map[string]bool{}
		for _, s := range retryable[cs.callee] {
			retry[s] = true
			if cs.table[s] != classRetryable {
				t.Errorf("%s: %s must be retryable", cs.callee, s)
			}
		}
		for _, s := range terminal[cs.callee] {
			if cs.table[s] != classTerminal {
				t.Errorf("%s: %s must be explicitly terminal", cs.callee, s)
			}
		}
		for slug, class := range cs.table {
			if class == classRetryable && !retry[slug] {
				t.Errorf("%s: %s is retryable in the table but not in the callee's ADR class table", cs.callee, slug)
			}
		}
	}
}
