package inbound

import (
	"sort"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// Level is a packaging level of a catalogue item.
type Level string

// The four packaging levels (fleet rule G6).
const (
	LevelEach   Level = "EACH"
	LevelInner  Level = "INNER"
	LevelCase   Level = "CASE"
	LevelPallet Level = "PALLET"
)

// order ranks the levels from the consumer unit upward.
func (l Level) order() int {
	if l == LevelEach {
		return 1
	}
	if l == LevelInner {
		return 2
	}
	if l == LevelCase {
		return 3
	}
	if l == LevelPallet {
		return 4
	}
	return 0
}

// Valid reports whether l is one of the four levels.
func (l Level) Valid() bool { return l.order() > 0 }

// PackLevel is one row of CatalogPackagingView: a GTIN declared at a level of
// a catalogue item's packaging hierarchy.
type PackLevel struct {
	CatalogItemID string
	Level         Level
	GTIN          string
	ChildGTIN     string
	UnitsPerPack  int
	Active        bool
	DeclaredAt    time.Time
	RetiredAt     time.Time
}

// Declared carries CatalogItemPackagingDeclared.
type Declared struct {
	CatalogItemID string
	Level         Level
	GTIN          string
	ChildGTIN     string
	UnitsPerPack  int
	DeclaredAt    time.Time
}

// Retired carries CatalogItemPackLevelRetired.
type Retired struct {
	CatalogItemID string
	Level         Level
	GTIN          string
	RetiredAt     time.Time
}

func checkLevelKey(catalogItemID string, l Level, gtin string) (string, error) {
	if !shared.ValidID(catalogItemID) {
		return "", shared.Invalid("catalogItemId must be a valid id")
	}
	if !l.Valid() {
		return "", shared.Invalid("level must be EACH, INNER, CASE or PALLET")
	}
	g, err := ParseGTIN(gtin)
	if err != nil {
		return "", shared.Invalid("%v", err)
	}
	return g, nil
}

// OnDeclared applies CatalogItemPackagingDeclared to the stored row (zero value
// when none): the GTIN becomes active at its level, unless a retirement newer
// than the declaration was already seen (events may arrive out of order).
func (p PackLevel) OnDeclared(e Declared) (PackLevel, error) {
	g, err := checkLevelKey(e.CatalogItemID, e.Level, e.GTIN)
	if err != nil {
		return PackLevel{}, err
	}
	child := ""
	if e.ChildGTIN != "" {
		if child, err = ParseGTIN(e.ChildGTIN); err != nil {
			return PackLevel{}, shared.Invalid("%v", err)
		}
	}
	if e.UnitsPerPack < 1 || e.UnitsPerPack > shared.MaxInt32 || (e.Level == LevelEach) != (e.UnitsPerPack == 1) {
		return PackLevel{}, shared.Invalid("unitsPerPack is 1 for EACH and at least 2 above it")
	}
	if e.DeclaredAt.IsZero() {
		return PackLevel{}, shared.Invalid("declaredAt is required")
	}
	out := PackLevel{CatalogItemID: e.CatalogItemID, Level: e.Level, GTIN: g, ChildGTIN: child, UnitsPerPack: e.UnitsPerPack,
		DeclaredAt: e.DeclaredAt.UTC(), RetiredAt: p.RetiredAt, Active: true}
	if !p.RetiredAt.IsZero() && !e.DeclaredAt.After(p.RetiredAt) {
		out.Active = false
	}
	return out, nil
}

// OnRetired applies CatalogItemPackLevelRetired: the GTIN stops being active.
// A retirement that predates the stored declaration is stale and changes nothing.
func (p PackLevel) OnRetired(e Retired) (PackLevel, error) {
	g, err := checkLevelKey(e.CatalogItemID, e.Level, e.GTIN)
	if err != nil {
		return PackLevel{}, err
	}
	if e.RetiredAt.IsZero() {
		return PackLevel{}, shared.Invalid("retiredAt is required")
	}
	out := p
	out.CatalogItemID, out.Level, out.GTIN = e.CatalogItemID, e.Level, g
	if p.DeclaredAt.IsZero() || e.RetiredAt.After(p.DeclaredAt) {
		out.Active = false
	}
	if e.RetiredAt.After(p.RetiredAt) {
		out.RetiredAt = e.RetiredAt.UTC()
	}
	return out, nil
}

// ActiveLevels returns the active rows ordered EACH, INNER, CASE, PALLET and
// then by GTIN: the packaging[] of a listing.
func ActiveLevels(rows []PackLevel) []PackLevel {
	out := make([]PackLevel, 0, len(rows))
	for _, r := range rows {
		if r.Active {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level.order() < out[j].Level.order()
		}
		return out[i].GTIN < out[j].GTIN
	})
	return out
}
