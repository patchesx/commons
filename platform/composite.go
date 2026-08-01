package platform

import (
	"context"
	"errors"
)

// CompositeNotifier fans out NotifyUser calls to all registered notifiers.
// Add notifiers with Add; the zero value is valid and has no notifiers.
type CompositeNotifier struct {
	notifiers []Notifier
}

// Add appends a notifier to the composite.
func (c *CompositeNotifier) Add(n Notifier) {
	c.notifiers = append(c.notifiers, n)
}

// NotifyUser delivers msg to userID through all registered notifiers.
// All notifiers are attempted even if one fails; all errors are joined.
func (c *CompositeNotifier) NotifyUser(ctx context.Context, userID string, msg Message) error {
	var errs []error
	for _, n := range c.notifiers {
		if err := n.NotifyUser(ctx, userID, msg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
