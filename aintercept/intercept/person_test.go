package intercept_test

import (
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept/intercepttest"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// A rule can tell a person the provider verified from one the client only
// claims to be: Person.Verified, and the verified account over the reported
// one.
func TestPersonVerified(t *testing.T) {
	var seen intercept.Person
	h := intercepttest.New(intercept.OnRequest(intercept.Prompt(nil)).Do(func(m *intercept.Message) intercept.Answer {
		seen = m.Call.Person
		if !m.Call.Person.Verified {
			return intercept.Reply("Sign in first.")
		}
		return intercept.Accept()
	}))
	h.Prompt("hi").WantReply(t, "Sign in first.")
	h.As(&pb.Identity{Email: "spoof@example.com", EmailSource: pb.IdentitySource_IDENTITY_SOURCE_ACCOUNT, ReportedAccountId: "acc-9"}).
		Prompt("hi").WantReply(t, "Sign in first.")
	if seen != (intercept.Person{Email: "spoof@example.com", Account: "acc-9"}) {
		t.Errorf("claimed person %+v", seen)
	}
	h.As(&pb.Identity{Email: "a@example.com", Name: "Ada", EmailSource: pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL,
		VerifiedAccountId: "acc-1", ReportedAccountId: "acc-9", AccountMismatch: true}).Prompt("hi").WantAccepted(t)
	if seen != (intercept.Person{Email: "a@example.com", Name: "Ada", Account: "acc-1", Verified: true}) {
		t.Errorf("verified person %+v", seen)
	}
}
