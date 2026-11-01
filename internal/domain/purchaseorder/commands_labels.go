package purchaseorder

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// labelState says whether the order can hold labels now: nil in ACCEPTED,
// ErrLabelBeforeAcceptance before acceptance, an illegal transition after.
func (p *PurchaseOrder) labelState() error {
	switch p.state {
	case Accepted:
		return nil
	case Issued, AckProcessing:
		return ErrLabelBeforeAcceptance
	default:
		return fmt.Errorf("%w: no label for an order in state %s", shared.ErrIllegalTransition, p.state)
	}
}

// LabelPlan splits the requested packages into those already labelled and
// those still to issue (RequestLabel, A7). Re-requesting only labelled
// packages is allowed in ACCEPTED, SHIPMENT_PROCESSING and SHIPPED.
func (p *PurchaseOrder) LabelPlan(packageNos []int) ([]Label, []int, error) {
	if err := checkPackageNos(packageNos); err != nil {
		return nil, nil, err
	}
	byPkg := map[int]Label{}
	for _, l := range p.labels {
		byPkg[l.PackageNo] = l
	}
	var have []Label
	var missing []int
	for _, n := range packageNos {
		if l, ok := byPkg[n]; ok {
			have = append(have, l)
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 && (p.state == ShipmentProcessing || p.state == Shipped) {
		return sortedLabels(have), nil, nil
	}
	if err := p.labelState(); err != nil {
		return nil, nil, err
	}
	return sortedLabels(have), missing, nil
}

func sortedLabels(ls []Label) []Label {
	slices.SortFunc(ls, func(a, b Label) int { return cmp.Compare(a.PackageNo, b.PackageNo) })
	return ls
}

func checkPackageNos(nos []int) error {
	if len(nos) == 0 || len(nos) > MaxPackages {
		return shared.Invalid("request 1 to %d packages", MaxPackages)
	}
	seen := map[int]bool{}
	for _, n := range nos {
		if n < 1 || n > MaxPackageNo {
			return shared.Invalid("packageNo must be from 1 to %d", MaxPackageNo)
		}
		if seen[n] {
			return shared.Invalid("packageNo %d is requested twice", n)
		}
		seen[n] = true
	}
	return nil
}

// AddLabels records issued labels on an ACCEPTED order. A label for a package
// that already holds the identical label is ignored; a different one, or a
// tracking number used twice, is a validation error.
func (p *PurchaseOrder) AddLabels(labels []Label) error {
	if err := p.labelState(); err != nil {
		return err
	}
	next := append([]Label(nil), p.labels...)
	changed := false
	for _, l := range labels {
		if err := checkLabel(l); err != nil {
			return shared.Invalid("%v", err)
		}
		dup, err := findLabel(next, l)
		if err != nil {
			return err
		}
		if !dup {
			next = append(next, l)
			changed = true
		}
	}
	if len(next) > MaxPackages {
		return shared.Invalid("at most %d packages", MaxPackages)
	}
	if changed {
		slices.SortFunc(next, func(a, b Label) int { return cmp.Compare(a.PackageNo, b.PackageNo) })
		p.labels = next
		p.version++
	}
	return nil
}

// findLabel reports whether l is already in labels; a clash is an error.
func findLabel(labels []Label, l Label) (bool, error) {
	for _, e := range labels {
		if e == l {
			return true, nil
		}
		if e.PackageNo == l.PackageNo || e.TrackingNumber == l.TrackingNumber {
			return false, shared.Invalid("package %d clashes with the label of package %d", l.PackageNo, e.PackageNo)
		}
	}
	return false, nil
}
