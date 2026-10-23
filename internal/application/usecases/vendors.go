package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

func vendorView(a *vendor.Account) views.VendorAccount {
	return views.VendorAccount{VendorID: string(a.ID()), Name: a.Name(), AckWindowMinutes: a.AckWindowMinutes(), State: string(a.State())}
}

// OnboardVendor creates an ACTIVE vendor account and publishes VendorOnboarded.
// Onboarding an existing vendorId returns the account unchanged and created
// false (assumption A8).
func (s *Service) OnboardVendor(ctx context.Context, in requests.OnboardVendor) (views.VendorAccount, bool, error) {
	if err := in.Validate(); err != nil {
		return views.VendorAccount{}, false, err
	}
	var out views.VendorAccount
	created := false
	err := s.atomically(ctx, func(ctx context.Context) error {
		created = false
		existing, err := s.d.Vendors.Get(ctx, vendor.ID(in.VendorID))
		if err == nil {
			out = vendorView(existing)
			return nil
		}
		if !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		a, evs, err := vendor.Onboard(vendor.NewAccount{ID: vendor.ID(in.VendorID), Name: in.Name, AckWindowMinutes: in.AckWindowMinutes}, s.d.Clock.Now())
		if err != nil {
			return err
		}
		if err := s.saveVendor(ctx, a, evs); err != nil {
			return err
		}
		out, created = vendorView(a), true
		return nil
	})
	return out, created && err == nil, err
}

func (s *Service) saveVendor(ctx context.Context, a *vendor.Account, evs []shared.Event) error {
	if err := s.d.Vendors.Save(ctx, a); err != nil {
		return err
	}
	return s.d.Outbox.Append(ctx, evs...)
}

// SuspendVendor moves the vendor to SUSPENDED and publishes VendorSuspended
// (Gap G30). Suspending a suspended vendor is a no-op (A8).
func (s *Service) SuspendVendor(ctx context.Context, vendorID, reason string) (views.VendorAccount, error) {
	return s.changeVendor(ctx, vendorID, func(a *vendor.Account) ([]shared.Event, error) {
		return a.Suspend(reason, s.d.Clock.Now())
	})
}

// ReinstateVendor moves the vendor back to ACTIVE and publishes VendorReinstated.
func (s *Service) ReinstateVendor(ctx context.Context, vendorID string) (views.VendorAccount, error) {
	return s.changeVendor(ctx, vendorID, func(a *vendor.Account) ([]shared.Event, error) {
		return a.Reinstate(s.d.Clock.Now())
	})
}

func (s *Service) changeVendor(ctx context.Context, vendorID string, cmd func(*vendor.Account) ([]shared.Event, error)) (views.VendorAccount, error) {
	var out views.VendorAccount
	err := s.atomically(ctx, func(ctx context.Context) error {
		a, err := s.d.Vendors.Get(ctx, vendor.ID(vendorID))
		if err != nil {
			return err
		}
		evs, err := cmd(a)
		if err != nil {
			return err
		}
		if len(evs) > 0 {
			if err := s.saveVendor(ctx, a, evs); err != nil {
				return err
			}
		}
		out = vendorView(a)
		return nil
	})
	return out, err
}

// GetVendor returns the vendor account.
func (s *Service) GetVendor(ctx context.Context, vendorID string) (views.VendorAccount, error) {
	a, err := s.d.Vendors.Get(ctx, vendor.ID(vendorID))
	if err != nil {
		return views.VendorAccount{}, err
	}
	return vendorView(a), nil
}

// requireActive loads the vendor and refuses a suspended one (ErrVendorSuspended).
// An unknown vendor is ports.ErrNotFound.
func (s *Service) requireActive(ctx context.Context, vendorID string) error {
	a, err := s.d.Vendors.Get(ctx, vendor.ID(vendorID))
	if err != nil {
		return err
	}
	return a.EnsureActive()
}

// requireVendor checks that the vendor exists (reads are allowed while suspended).
func (s *Service) requireVendor(ctx context.Context, vendorID string) error {
	_, err := s.d.Vendors.Get(ctx, vendor.ID(vendorID))
	return err
}
