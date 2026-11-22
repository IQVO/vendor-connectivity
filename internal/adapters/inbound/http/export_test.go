package http

import "net/http"

// CatalogueRow is one problem type of the catalogue as the external tests
// (the replay-class adapter) read it.
type CatalogueRow struct {
	Err    error
	Slug   string
	Status int
	Class  string
}

// CatalogueForTest returns every entry of the production problem catalogue
// with the replay class it declares.
func CatalogueForTest() []CatalogueRow {
	rows := make([]CatalogueRow, 0, len(problemCatalogue))
	for _, e := range problemCatalogue {
		rows = append(rows, CatalogueRow{Err: e.err, Slug: e.p.slug, Status: e.p.status, Class: e.replay.String()})
	}
	return rows
}

// WriteErrorForTest answers err through the production error writer, so the
// replay class of its problem marks the capturing writer as in production.
func WriteErrorForTest(w http.ResponseWriter, r *http.Request, err error) { writeError(w, r, err) }
