package harnessexecution

import (
	"errors"
	"testing"
)

func TestGateAdmitsWhileOpen(t *testing.T) {
	gate, control := CreateGate()
	if err := gate.Admit(func() error { return nil }); err != nil {
		t.Fatalf("open admit = %v", err)
	}
	control.Close(errors.New("closed"))
	if err := gate.Admit(func() error { return nil }); err == nil || err.Error() != "closed" {
		t.Fatalf("closed admit = %v", err)
	}
}

func TestGateAbortWinsAdmission(t *testing.T) {
	gate, control := CreateGate()
	cancellation := make(chan struct{})
	control.BeginAbort(cancellation)
	control.SignalAbort()
	err := gate.Admit(func() error { return nil })
	var abort *AbortRequested
	if !errors.As(err, &abort) {
		t.Fatalf("expected AbortRequested, got %v", err)
	}
	if abort.Cancellation() != cancellation {
		t.Fatal("AbortRequested lost the cancellation channel")
	}
	select {
	case <-gate.Signal():
	default:
		t.Fatal("signal should be closed after SignalAbort")
	}
	if err := gate.Admit(func() error { return nil }); err == nil {
		t.Fatal("aborting gate must reject admission")
	}
}

func TestGateCloseIsTerminal(t *testing.T) {
	gate, control := CreateGate()
	terminal := errors.New("terminal")
	control.Close(terminal)
	control.Close(errors.New("second"))
	if err := gate.Admit(func() error { return nil }); !errors.Is(err, terminal) {
		t.Fatalf("admit = %v", err)
	}
	select {
	case <-gate.Signal():
	default:
		t.Fatal("close must settle the signal")
	}
}
