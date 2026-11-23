package delivery

import "testing"

func TestError_SinglePartShowsDetailOnly(t *testing.T) {
	if got := (&Error{Detail: "HTTP 502"}).Error(); got != "HTTP 502" {
		t.Fatalf("got %q", got)
	}
}

func TestError_MultiPartReportsHowMuchWasDelivered(t *testing.T) {
	got := (&Error{Detail: "HTTP 500", Sent: 2, Total: 5}).Error()
	if got != "HTTP 500 (delivered 2 of 5 parts)" {
		t.Fatalf("got %q", got)
	}
}
