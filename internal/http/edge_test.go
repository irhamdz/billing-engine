package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Edge cases for HTTP error mapping that aren't covered by happy-path tests.

func TestHTTP_CreateLoan_MalformedJSON(t *testing.T) {
	srv := newServer(t)
	resp := doReq(t, srv, "POST", "/v1/loans", `{"principal": "not-a-number"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_CreateLoan_BadStartDate(t *testing.T) {
	srv := newServer(t)
	body := `{"borrower_id":"11111111-1111-1111-1111-111111111111","principal":5000000,"annual_interest_rate":0.10,"term_weeks":50,"start_date":"not-a-date"}`
	resp := doReq(t, srv, "POST", "/v1/loans", body, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_CreateLoan_BadBorrowerID(t *testing.T) {
	srv := newServer(t)
	body := `{"borrower_id":"not-a-uuid","principal":5000000,"annual_interest_rate":0.10,"term_weeks":50,"start_date":"2026-05-06"}`
	resp := doReq(t, srv, "POST", "/v1/loans", body, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_BadLoanID(t *testing.T) {
	srv := newServer(t)
	resp := doReq(t, srv, "GET", "/v1/loans/not-a-uuid/outstanding", "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_MakePayment_MissingIdempotencyKey(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Idempotency-Key") {
		t.Fatalf("body=%s", body)
	}
}

func TestHTTP_MakePayment_MalformedJSON(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{`,
		map[string]string{"Idempotency-Key": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_GetDelinquency_BadAsOf(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/delinquency?as_of=garbage", "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_GetDelinquency_NoAsOf(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/delinquency", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_LoanNotFound_AcrossEndpoints(t *testing.T) {
	srv := newServer(t)
	missing := "00000000-0000-0000-0000-000000000000"
	for _, path := range []string{
		"/v1/loans/" + missing,
		"/v1/loans/" + missing + "/schedule",
		"/v1/loans/" + missing + "/payments",
		"/v1/loans/" + missing + "/delinquency",
	} {
		resp := doReq(t, srv, "GET", path, "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
