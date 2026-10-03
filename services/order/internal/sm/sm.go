package sm

import "fmt"

type Status string

const (
	Created         Status = "created"
	AwaitingPayment Status = "awaiting_payment"
	PaymentFailed   Status = "payment_failed"
	Paid            Status = "paid"
	Dispatched      Status = "dispatched"
	Delivered       Status = "delivered"
	Cancelled       Status = "cancelled"
)

// transitions is the single source of truth for legal order state changes.
// Consumers apply events through CanTransition, so replayed/duplicate
// events become harmless no-ops.
var transitions = map[Status]map[Status]bool{
	Created:         {AwaitingPayment: true, PaymentFailed: true, Paid: true, Cancelled: true},
	AwaitingPayment: {Paid: true, PaymentFailed: true, Cancelled: true},
	PaymentFailed:   {AwaitingPayment: true, Paid: true, Cancelled: true},
	Paid:            {Dispatched: true, Delivered: true},
	Dispatched:      {Delivered: true},
	Delivered:       {},
	Cancelled:       {},
}

func Parse(s string) (Status, error) {
	st := Status(s)
	if _, ok := transitions[st]; !ok {
		return "", fmt.Errorf("unknown order status %q", s)
	}
	return st, nil
}

func CanTransition(from, to Status) bool {
	return transitions[from][to]
}

func (s Status) IsFinal() bool { return s == Delivered || s == Cancelled }

// Cancellable reports whether the API may cancel right now.
func (s Status) Cancellable() bool {
	return s == Created || s == AwaitingPayment || s == PaymentFailed
}
