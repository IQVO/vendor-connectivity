package memory

import (
	"context"
	"sort"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// versionOK applies the optimistic-concurrency rule of the ports package:
// stored version == version-1, and a create (version 1) expects no row.
func versionOK(stored int64, exists bool, version int64) bool {
	if version == 1 {
		return !exists
	}
	return exists && stored == version-1
}

// VendorRepository is the in-memory ports.VendorRepository.
type VendorRepository struct{ s *Store }

// Get returns the account or ports.ErrNotFound.
func (r *VendorRepository) Get(ctx context.Context, id vendor.ID) (*vendor.Account, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.vendors[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return vendor.Restore(snap)
}

// Save stores the account under the concurrency rule.
func (r *VendorRepository) Save(ctx context.Context, a *vendor.Account) error {
	defer r.s.enter(ctx)()
	old, ok := r.s.vendors[a.ID()]
	if !versionOK(old.Version, ok, a.Version()) {
		return ports.ErrConcurrentUpdate
	}
	r.s.vendors[a.ID()] = a.Snapshot()
	return nil
}

// PurchaseOrderRepository is the in-memory ports.PurchaseOrderRepository.
type PurchaseOrderRepository struct{ s *Store }

// Get returns the order or ports.ErrNotFound.
func (r *PurchaseOrderRepository) Get(ctx context.Context, n purchaseorder.Number) (*purchaseorder.PurchaseOrder, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.orders[n]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return purchaseorder.Restore(snap)
}

// FindByRelease returns the order of (assignmentId, releaseNo) or ports.ErrNotFound.
func (r *PurchaseOrderRepository) FindByRelease(ctx context.Context, assignmentID string, releaseNo int) (*purchaseorder.PurchaseOrder, error) {
	defer r.s.enter(ctx)()
	for _, snap := range r.s.orders {
		if snap.AssignmentID == assignmentID && snap.ReleaseNo == releaseNo {
			return purchaseorder.Restore(snap)
		}
	}
	return nil, ports.ErrNotFound
}

// FindLatestByAssignment returns the order with the highest releaseNo of the assignment.
func (r *PurchaseOrderRepository) FindLatestByAssignment(ctx context.Context, assignmentID string) (*purchaseorder.PurchaseOrder, error) {
	defer r.s.enter(ctx)()
	var best *purchaseorder.Snapshot
	for _, snap := range r.s.orders {
		if snap.AssignmentID == assignmentID && (best == nil || snap.ReleaseNo > best.ReleaseNo) {
			snap := snap
			best = &snap
		}
	}
	if best == nil {
		return nil, ports.ErrNotFound
	}
	return purchaseorder.Restore(*best)
}

func after(s purchaseorder.Snapshot, c repository.Cursor) bool {
	if s.IssuedAt.Equal(c.IssuedAt) {
		return string(s.Number) > c.PONumber
	}
	return s.IssuedAt.After(c.IssuedAt)
}

func matches(s purchaseorder.Snapshot, f repository.PurchaseOrderFilter) bool {
	switch {
	case f.VendorID != "" && s.VendorID != f.VendorID:
		return false
	case f.NodeID != "" && s.NodeID != f.NodeID:
		return false
	case f.IssuedAfter != nil && !s.IssuedAt.After(*f.IssuedAfter):
		return false
	case f.After != nil && !after(s, *f.After):
		return false
	}
	return true
}

// List returns the matching orders ordered by (issuedAt, poNumber).
func (r *PurchaseOrderRepository) List(ctx context.Context, f repository.PurchaseOrderFilter) ([]*purchaseorder.PurchaseOrder, error) {
	defer r.s.enter(ctx)()
	var snaps []purchaseorder.Snapshot
	for _, snap := range r.s.orders {
		if matches(snap, f) {
			snaps = append(snaps, snap)
		}
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].IssuedAt.Equal(snaps[j].IssuedAt) {
			return snaps[i].Number < snaps[j].Number
		}
		return snaps[i].IssuedAt.Before(snaps[j].IssuedAt)
	})
	if f.Limit > 0 && len(snaps) > f.Limit {
		snaps = snaps[:f.Limit]
	}
	return restoreOrders(snaps)
}

func restoreOrders(snaps []purchaseorder.Snapshot) ([]*purchaseorder.PurchaseOrder, error) {
	out := make([]*purchaseorder.PurchaseOrder, 0, len(snaps))
	for _, snap := range snaps {
		p, err := purchaseorder.Restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ListExpirable returns ISSUED orders whose ackBy is before now, oldest ackBy first.
func (r *PurchaseOrderRepository) ListExpirable(ctx context.Context, now time.Time, limit int) ([]*purchaseorder.PurchaseOrder, error) {
	defer r.s.enter(ctx)()
	var snaps []purchaseorder.Snapshot
	for _, snap := range r.s.orders {
		if snap.State == purchaseorder.Issued && snap.AckBy.Before(now) {
			snaps = append(snaps, snap)
		}
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].AckBy.Equal(snaps[j].AckBy) {
			return snaps[i].Number < snaps[j].Number
		}
		return snaps[i].AckBy.Before(snaps[j].AckBy)
	})
	if limit > 0 && len(snaps) > limit {
		snaps = snaps[:limit]
	}
	return restoreOrders(snaps)
}

// Save stores the order under the concurrency rule; a second order for one
// release is ports.ErrConcurrentUpdate.
func (r *PurchaseOrderRepository) Save(ctx context.Context, p *purchaseorder.PurchaseOrder) error {
	defer r.s.enter(ctx)()
	old, ok := r.s.orders[p.Number()]
	if !versionOK(old.Version, ok, p.Version()) {
		return ports.ErrConcurrentUpdate
	}
	r.s.orders[p.Number()] = p.Snapshot()
	return nil
}

// SubmissionRepository is the in-memory ports.SubmissionRepository.
type SubmissionRepository struct{ s *Store }

// Get returns the submission or ports.ErrNotFound.
func (r *SubmissionRepository) Get(ctx context.Context, id submission.ID) (*submission.Submission, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.subs[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return submission.Restore(snap)
}

// ListDue returns PROCESSING submissions whose notBefore is not after now, oldest first.
func (r *SubmissionRepository) ListDue(ctx context.Context, now time.Time, limit int) ([]*submission.Submission, error) {
	defer r.s.enter(ctx)()
	var snaps []submission.Snapshot
	for _, snap := range r.s.subs {
		if snap.Status == submission.Processing && !snap.NotBefore.After(now) {
			snaps = append(snaps, snap)
		}
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].NotBefore.Equal(snaps[j].NotBefore) {
			return snaps[i].ID < snaps[j].ID
		}
		return snaps[i].NotBefore.Before(snaps[j].NotBefore)
	})
	if limit > 0 && len(snaps) > limit {
		snaps = snaps[:limit]
	}
	out := make([]*submission.Submission, 0, len(snaps))
	for _, snap := range snaps {
		s, err := submission.Restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Save stores the submission under the concurrency rule.
func (r *SubmissionRepository) Save(ctx context.Context, sub *submission.Submission) error {
	defer r.s.enter(ctx)()
	old, ok := r.s.subs[sub.ID()]
	if !versionOK(old.Version, ok, sub.Version()) {
		return ports.ErrConcurrentUpdate
	}
	r.s.subs[sub.ID()] = sub.Snapshot()
	return nil
}

// DeleteProcessedBefore removes processed submissions processed before t.
func (r *SubmissionRepository) DeleteProcessedBefore(ctx context.Context, t time.Time) (int, error) {
	defer r.s.enter(ctx)()
	n := 0
	for id, snap := range r.s.subs {
		if snap.Status != submission.Processing && snap.ProcessedAt.Before(t) {
			delete(r.s.subs, id)
			n++
		}
	}
	return n, nil
}

// NodeViewRepository is the in-memory ports.NodeViewRepository.
type NodeViewRepository struct{ s *Store }

// Get returns the node or ports.ErrNotFound.
func (r *NodeViewRepository) Get(ctx context.Context, nodeID string) (network.Node, error) {
	defer r.s.enter(ctx)()
	n, ok := r.s.nodes[nodeID]
	if !ok {
		return network.Node{}, ports.ErrNotFound
	}
	return n, nil
}

// ListByVendor returns the vendor's nodes ordered by id.
func (r *NodeViewRepository) ListByVendor(ctx context.Context, vendorID string) ([]network.Node, error) {
	defer r.s.enter(ctx)()
	var out []network.Node
	for _, n := range r.s.nodes {
		if n.Operator == network.OperatorVendor && n.VendorID == vendorID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Save inserts or replaces the node.
func (r *NodeViewRepository) Save(ctx context.Context, n network.Node) error {
	defer r.s.enter(ctx)()
	r.s.nodes[n.ID] = n
	return nil
}

// ListingViewRepository is the in-memory ports.ListingViewRepository.
type ListingViewRepository struct{ s *Store }

// Get returns the listing or ports.ErrNotFound.
func (r *ListingViewRepository) Get(ctx context.Context, listingID string) (network.Listing, error) {
	defer r.s.enter(ctx)()
	l, ok := r.s.listings[listingID]
	if !ok {
		return network.Listing{}, ports.ErrNotFound
	}
	return l, nil
}

// Save inserts or replaces the listing.
func (r *ListingViewRepository) Save(ctx context.Context, l network.Listing) error {
	defer r.s.enter(ctx)()
	r.s.listings[l.ID] = l
	return nil
}

// Inbox is the in-memory ports.ProcessedEvents.
type Inbox struct{ s *Store }

// Claim records the event id and reports whether it was new.
func (i *Inbox) Claim(ctx context.Context, eventID, eventType string, _ time.Time) (bool, error) {
	defer i.s.enter(ctx)()
	if _, seen := i.s.inbox[eventID]; seen {
		return false, nil
	}
	i.s.inbox[eventID] = eventType
	return true, nil
}

// Outbox is the in-memory ports.EventOutbox.
type Outbox struct{ s *Store }

// Append stores the events in order.
func (o *Outbox) Append(ctx context.Context, events ...shared.Event) error {
	defer o.s.enter(ctx)()
	o.s.outbox = append(o.s.outbox, events...)
	return nil
}

// Events returns the appended events in order (a copy of the slice).
func (o *Outbox) Events() []shared.Event {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	return append([]shared.Event(nil), o.s.outbox...)
}

// UnitOfWork is the in-memory ports.UnitOfWork: serialised, atomic, re-entrant.
type UnitOfWork struct{ s *Store }

// Do runs fn holding the store. When fn returns an error or panics the state
// taken before fn is restored; the error is returned unchanged. A Do inside a Do
// joins it.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if ctx.Value(txKey{u.s}) != nil {
		return fn(ctx)
	}
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	before := u.s.capture()
	committed := false
	defer func() {
		if !committed {
			u.s.restore(before)
		}
	}()
	err = fn(context.WithValue(ctx, txKey{u.s}, struct{}{}))
	committed = err == nil
	return err
}
