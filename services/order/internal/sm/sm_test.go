package sm

import "testing"

func TestTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		want     bool
	}{
		{Created, AwaitingPayment, true},
		{Created, Paid, true}, // payment may auto-capture in one step
		{Created, Cancelled, true},
		{Created, Dispatched, false},
		{Created, Delivered, false},
		{AwaitingPayment, Paid, true},
		{AwaitingPayment, PaymentFailed, true},
		{AwaitingPayment, Cancelled, true},
		{PaymentFailed, AwaitingPayment, true}, // retry
		{PaymentFailed, Cancelled, true},
		{Paid, Dispatched, true},
		{Paid, Cancelled, false}, // no auto-cancel after capture
		{Dispatched, Delivered, true},
		{Dispatched, Paid, false},
		{Delivered, Dispatched, false},
		{Cancelled, Paid, false}, // terminal
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Errorf("CanTransition(%s → %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	if _, err := Parse("paid"); err != nil {
		t.Fatalf("Parse(paid): %v", err)
	}
	if _, err := Parse("bogus"); err == nil {
		t.Fatal("Parse(bogus) should fail")
	}
}

func TestCancellableAndFinal(t *testing.T) {
	for _, s := range []Status{Created, AwaitingPayment, PaymentFailed} {
		if !s.Cancellable() {
			t.Errorf("%s should be cancellable", s)
		}
	}
	for _, s := range []Status{Paid, Dispatched, Delivered} {
		if s.Cancellable() {
			t.Errorf("%s should not be cancellable", s)
		}
	}
	if !Delivered.IsFinal() || !Cancelled.IsFinal() {
		t.Error("Delivered and Cancelled must be final")
	}
}
