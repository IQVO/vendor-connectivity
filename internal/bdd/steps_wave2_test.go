package bdd_test

// Step vocabulary of the coverage wave: node-profile and scorecard relays,
// poll pagination, and assertions on the published outbox events' payload
// fields. Everything reuses the world and the doubles the suite already
// wires; the node-performance fake answers 404 (the gateway client's
// Refusal) exactly as the real adapter does for a node without observations.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

func registerWave2(sc *godog.ScenarioContext, w *world) {
	// given
	sc.Step(`^node-performance measured grade "([^"]*)" over the last "([^"]*)"$`, func(grade, window string) error {
		w.perf.Scorecard = views.Scorecard{Windows: []views.ScorecardWindow{{Window: window, Grade: grade, Observations: 25}}}
		return nil
	})
	sc.Step(`^node-performance has no observations yet$`, func() error {
		w.perf.Err = &gateway.Refusal{Status: 404, Code: "ErrNoScorecard", Message: "the node has no observations yet"}
		return nil
	})
	sc.Step(`^node-performance cannot be reached$`, func() error {
		w.perf.Err = errors.New("connection reset by peer")
		return nil
	})

	// when
	sc.Step(`^(1) (minute|hour|second) passes$`, func(n int, unit string) error {
		d := map[string]time.Duration{"second": time.Second, "minute": time.Minute, "hour": time.Hour}[unit]
		w.clock.Advance(time.Duration(n) * d)
		return nil
	})
	sc.Step(`^I remember the tracking numbers of the issued labels$`, w.rememberTrackingNumbers)
	sc.Step(`^I poll again with the returned nextToken for vendor "([^"]*)"$`, func(vendor string) error {
		tok, ok, err := w.field("nextToken")
		if err != nil {
			return err
		}
		if !ok || render(tok) == "" {
			return fmt.Errorf("the previous page carried no nextToken: %s", w.body)
		}
		return w.request(http.MethodGet, "/vendor/purchase-orders?vendorId="+vendor+"&nextToken="+render(tok), "")
	})

	// then
	sc.Step(`^the response field "([^"]*)" is present$`, func(path string) error {
		if _, ok, err := w.field(path); err != nil || !ok {
			return fmt.Errorf("%s is absent in %s", path, w.body)
		}
		return nil
	})
	sc.Step(`^node-topology received (\d+) DeclareOperationalProfile calls?$`, func(n int) error {
		if len(w.nt.Profiles) != n {
			return fmt.Errorf("DeclareOperationalProfile calls = %d, want %d", len(w.nt.Profiles), n)
		}
		return nil
	})
	sc.Step(`^node-topology received the profile revision (\d+)$`, func(rev int) error {
		if len(w.nt.Profiles) == 0 {
			return errors.New("node-topology received no profile declaration")
		}
		if got := w.nt.Profiles[len(w.nt.Profiles)-1].ProfileRevision; got != rev {
			return fmt.Errorf("declared revision = %d, want %d", got, rev)
		}
		return nil
	})
	sc.Step(`^(\d+) "([^"]*)" events? were published$`, func(n int, name string) error {
		if got := len(w.eventsNamed(name)); got != n {
			return fmt.Errorf("%q events = %d, want %d (outbox: %v)", name, got, n, w.eventNames())
		}
		return nil
	})
	sc.Step(`^the "([^"]*)" event about "([^"]*)" carries "([^"]*)" "([^"]*)"$`, func(name, subject, field, want string) error {
		want = w.expand(want)
		got, ok := w.eventField(w.expand(name), w.expand(subject), field)
		if !ok {
			return fmt.Errorf("no %q event about %q carrying %q in %v", name, subject, field, w.eventNames())
		}
		if got != want {
			return fmt.Errorf("%s.%s = %q, want %q", name, field, got, want)
		}
		return nil
	})
}

// eventsNamed returns the outbox events with the given EventName.
func (w *world) eventsNamed(name string) []any {
	var out []any
	for _, ev := range w.store.Outbox().Events() {
		if ev.EventName() == name {
			out = append(out, ev)
		}
	}
	return out
}

// eventField finds the event with the given name and subject and renders one
// of its struct fields (case-insensitive, embedded headers included).
func (w *world) eventField(name, subject, field string) (string, bool) {
	for _, ev := range w.store.Outbox().Events() {
		if ev.EventName() != name || ev.SubjectID() != subject {
			continue
		}
		return structField(reflect.ValueOf(ev), field)
	}
	return "", false
}

// structField renders the field of a (possibly pointer) struct value matched
// case-insensitively, descending into anonymous embedded structs.
func structField(v reflect.Value, field string) (string, bool) {
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && v.Field(i).Kind() == reflect.Struct {
			if got, ok := structField(v.Field(i), field); ok {
				return got, true
			}
			continue
		}
		if strings.EqualFold(f.Name, field) {
			return renderValue(v.Field(i)), true
		}
	}
	return "", false
}

func renderValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Slice:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, renderValue(v.Index(i)))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(v.Interface())
	}
}

// rememberTrackingNumbers records the tracking number of every label of the
// last response, so later steps can reference them as {trk:<packageNo>}.
func (w *world) rememberTrackingNumbers() error {
	var page struct {
		Labels []struct {
			PackageNo      int    `json:"packageNo"`
			TrackingNumber string `json:"trackingNumber"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(w.body, &page); err != nil {
		return fmt.Errorf("the last response carries no labels: %s", w.body)
	}
	if len(page.Labels) == 0 {
		return fmt.Errorf("no labels in %s", w.body)
	}
	if w.lastLabels == nil {
		w.lastLabels = map[int]string{}
	}
	for _, l := range page.Labels {
		w.lastLabels[l.PackageNo] = l.TrackingNumber
	}
	return nil
}

// expandLabels replaces every {trk:<packageNo>} placeholder with the tracking
// number remembered from an earlier label response.
func (w *world) expandLabels(s string) string {
	for {
		i := strings.Index(s, "{trk:")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return s
		}
		pkg, err := strconv.Atoi(s[i+5 : i+j])
		if err != nil {
			return s
		}
		s = s[:i] + w.lastLabels[pkg] + s[i+j+1:]
	}
}
