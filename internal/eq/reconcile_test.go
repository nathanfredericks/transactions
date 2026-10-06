package eq

import (
	"context"
	"encoding/json"
	"github.com/nathanfredericks/transactions/internal/email"
	"testing"
)

func ptr[T any](v T) *T { return &v }
func TestPurchaseIdentityAndFXReconciliation(t *testing.T) {
	a := Record{AccountID: "account", Amount: -8300, Date: "2026-10-05", Description: "TAILSCALE", OriginalAmount: ptr(int64(-5000)), OriginalCurrency: ptr("USD"), ProvisionalID: "pending", Status: "pending"}
	b := a
	b.ProvisionalID = "posted"
	b.Amount = -8330
	b.Date = "2026-10-06"
	if !samePurchase(a, b) {
		t.Fatal("same foreign purchase not recognized")
	}
	b.AccountID = "other"
	if samePurchase(a, b) {
		t.Fatal("different accounts matched")
	}
	b = a
	b.OriginalAmount = ptr(int64(-6000))
	if samePurchase(a, b) {
		t.Fatal("different original amount matched")
	}
	b = a
	b.Date = "2026-11-05"
	if samePurchase(a, b) {
		t.Fatal("date outside window matched")
	}
	if !near("2026-10-05", "2026-10-06", 1) || near("bad", "2026-10-06", 1) {
		t.Fatal("date matching")
	}
	b = a
	b.ProvisionalID = "posted"
	a.References = []string{"ref"}
	b.References = []string{"ref"}
	if !exactMatch(a, b) {
		t.Fatal("bank references ignored")
	}
	if identity(a) != "pending" {
		t.Fatal("wrong identity")
	}
	a.BankID = ptr("native")
	if identity(a) != "native" {
		t.Fatal("native identity ignored")
	}
}
func TestJobEnvelopeAndAllowlist(t *testing.T) {
	valid := Job{Version: 1, JobID: "job", Source: "manual"}
	if e := validate(valid); e != nil {
		t.Fatal(e)
	}
	for _, job := range []Job{{}, {Version: 1, JobID: "bad/path", Source: "manual"}, {Version: 1, JobID: "job", Source: "unknown"}, {Version: 1, JobID: "job", Source: "alert"}, {Version: 1, JobID: "job", Source: "session", Purpose: "retrieve"}} {
		if validate(job) == nil {
			t.Fatal("invalid job accepted", job)
		}
	}
	for _, p := range []*email.ParsedEmail{{From: "wrong", Subject: "Purchase made on your EQ Bank Card"}, {From: "alert@eqbank.ca", Subject: "Sign in"}, {From: "alert@eqbank.ca", Subject: "Purchase made on your EQ Bank Card"}} {
		if _, e := StartAlert(context.Background(), p, "A $5.00 purchase has been made on your EQ Bank Card."); e == nil {
			t.Fatal("unsafe alert accepted")
		}
	}
	if _, e := Handle(context.Background(), json.RawMessage(`bad`)); e == nil {
		t.Fatal("bad JSON accepted")
	}
}
func TestDryRunMaintenanceDoesNotWrite(t *testing.T) {
	s := &Store{}
	if e := s.resetMaintenance(context.Background(), Job{DryRun: true}, true); e != nil {
		t.Fatal(e)
	}
	if e := s.resetMaintenance(context.Background(), Job{Source: "alert"}, false); e != nil {
		t.Fatal(e)
	}
}
