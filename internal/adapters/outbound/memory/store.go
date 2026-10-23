// Package memory holds in-memory TEST DOUBLES of the application's out ports.
// They are for unit, BDD and contract tests only: cmd/** never imports this
// package (enforced by internal/architecture) because the service runs on real
// Postgres and Kafka.
//
// Every repository round-trips its aggregate through Snapshot and Restore, so a
// loaded aggregate never aliases stored state, a corrupt row can not become a
// live aggregate and the doubles keep the persistence contract of the real
// adapters: a miss is ports.ErrNotFound, a stale or duplicate save is
// ports.ErrConcurrentUpdate.
//
// One Store backs every double. Its UnitOfWork is atomic: it holds the store for
// the whole unit and restores the state taken at the start when the unit fails
// or panics, outbox and inbox included.
package memory

import (
	"context"
	"sync"

	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// Store is the shared state of the in-memory doubles.
type Store struct {
	mu       sync.Mutex
	vendors  map[vendor.ID]vendor.Snapshot
	orders   map[purchaseorder.Number]purchaseorder.Snapshot
	subs     map[submission.ID]submission.Snapshot
	nodes    map[string]network.Node
	listings map[string]network.Listing
	inbox    map[string]string
	outbox   []shared.Event
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		vendors: map[vendor.ID]vendor.Snapshot{}, orders: map[purchaseorder.Number]purchaseorder.Snapshot{},
		subs: map[submission.ID]submission.Snapshot{}, nodes: map[string]network.Node{}, listings: map[string]network.Listing{},
		inbox: map[string]string{},
	}
}

// state is a copy of everything a unit of work can change. Stored snapshots are
// replaced, never edited in place, so copying the maps is enough.
type state struct {
	vendors  map[vendor.ID]vendor.Snapshot
	orders   map[purchaseorder.Number]purchaseorder.Snapshot
	subs     map[submission.ID]submission.Snapshot
	nodes    map[string]network.Node
	listings map[string]network.Listing
	inbox    map[string]string
	outbox   []shared.Event
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) capture() state {
	return state{
		vendors: cloneMap(s.vendors), orders: cloneMap(s.orders), subs: cloneMap(s.subs), nodes: cloneMap(s.nodes),
		listings: cloneMap(s.listings), inbox: cloneMap(s.inbox), outbox: append([]shared.Event(nil), s.outbox...),
	}
}

func (s *Store) restore(st state) {
	s.vendors, s.orders, s.subs, s.nodes, s.listings, s.inbox, s.outbox = st.vendors, st.orders, st.subs, st.nodes, st.listings, st.inbox, st.outbox
}

// txKey marks a ctx that carries the open unit of work of one store.
type txKey struct{ store *Store }

// enter locks the store for one repository call, unless ctx already carries the
// unit of work that holds it. The returned func releases the lock.
func (s *Store) enter(ctx context.Context) func() {
	if ctx.Value(txKey{s}) != nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

// Counts is how many rows each table of the store holds.
type Counts struct {
	Vendors, Orders, Submissions, Nodes, Listings, Inbox, OutboxEvents int
	// VersionSum is the sum of every aggregate version: any saved change moves it.
	VersionSum int64
}

// Counts returns the size of every table and the sum of the aggregate versions,
// so a test can prove that a failed use case stored and changed nothing.
func (s *Store) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := Counts{Vendors: len(s.vendors), Orders: len(s.orders), Submissions: len(s.subs), Nodes: len(s.nodes), Listings: len(s.listings),
		Inbox: len(s.inbox), OutboxEvents: len(s.outbox)}
	for _, v := range s.vendors {
		c.VersionSum += v.Version
	}
	for _, o := range s.orders {
		c.VersionSum += o.Version
	}
	for _, sub := range s.subs {
		c.VersionSum += sub.Version
	}
	return c
}

// UnitOfWork returns the UnitOfWork double.
func (s *Store) UnitOfWork() *UnitOfWork { return &UnitOfWork{s} }

// Vendors returns the VendorRepository double.
func (s *Store) Vendors() *VendorRepository { return &VendorRepository{s} }

// Orders returns the PurchaseOrderRepository double.
func (s *Store) Orders() *PurchaseOrderRepository { return &PurchaseOrderRepository{s} }

// Submissions returns the SubmissionRepository double.
func (s *Store) Submissions() *SubmissionRepository { return &SubmissionRepository{s} }

// Nodes returns the NodeViewRepository double.
func (s *Store) Nodes() *NodeViewRepository { return &NodeViewRepository{s} }

// Listings returns the ListingViewRepository double.
func (s *Store) Listings() *ListingViewRepository { return &ListingViewRepository{s} }

// Inbox returns the ProcessedEvents double.
func (s *Store) Inbox() *Inbox { return &Inbox{s} }

// Outbox returns the EventOutbox double.
func (s *Store) Outbox() *Outbox { return &Outbox{s} }
