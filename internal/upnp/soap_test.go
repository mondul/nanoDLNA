package upnp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const browseEnvelope = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">
      <ObjectID>0</ObjectID>
      <BrowseFlag>BrowseDirectChildren</BrowseFlag>
      <Filter>*</Filter>
      <StartingIndex>0</StartingIndex>
      <RequestedCount>0</RequestedCount>
      <SortCriteria></SortCriteria>
    </u:Browse>
  </s:Body>
</s:Envelope>`

func soapRequestFor(t *testing.T, soapAction, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/ContentDirectory/control", strings.NewReader(body))
	r.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	if soapAction != "" {
		r.Header.Set("SOAPACTION", `"`+soapAction+`"`)
	}
	return r
}

func TestParseSOAPRequestFromBody(t *testing.T) {
	req, err := parseSOAPRequest(soapRequestFor(t, "", browseEnvelope), serviceContentDirectory)
	if err != nil {
		t.Fatalf("parseSOAPRequest: %v", err)
	}
	if req.Action != "Browse" {
		t.Errorf("Action = %q, want Browse", req.Action)
	}
	for name, want := range map[string]string{
		"ObjectID": "0", "BrowseFlag": "BrowseDirectChildren", "Filter": "*",
		"StartingIndex": "0", "RequestedCount": "0", "SortCriteria": "",
	} {
		if got := req.Args[name]; got != want {
			t.Errorf("arg %s = %q, want %q", name, got, want)
		}
	}
}

func TestParseSOAPRequestPrefersHeaderAction(t *testing.T) {
	req, err := parseSOAPRequest(
		soapRequestFor(t, "urn:schemas-upnp-org:service:ContentDirectory:1#GetSystemUpdateID", browseEnvelope),
		serviceContentDirectory)
	if err != nil {
		t.Fatalf("parseSOAPRequest: %v", err)
	}
	if req.Action != "GetSystemUpdateID" {
		t.Errorf("Action = %q, want the action named in SOAPACTION", req.Action)
	}
}

func TestParseSOAPRequestRejectsGarbage(t *testing.T) {
	for name, body := range map[string]string{
		"empty":     "",
		"not xml":   "<<<>>>",
		"no action": `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body></s:Body></s:Envelope>`,
	} {
		if _, err := parseSOAPRequest(soapRequestFor(t, "", body), serviceContentDirectory); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseSOAPRequestLimitsBodySize(t *testing.T) {
	huge := strings.Repeat("x", maxSOAPBody+1024)
	_, err := parseSOAPRequest(soapRequestFor(t, "", huge), serviceContentDirectory)
	if err == nil {
		t.Fatal("expected an error for an oversized body")
	}
}

func TestActionFromHeader(t *testing.T) {
	for in, want := range map[string]string{
		`"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`: "Browse",
		`urn:schemas-upnp-org:service:ContentDirectory:1#Browse`:   "Browse",
		`Browse`: "Browse",
		``:       "",
		`"  "`:   "",
	} {
		if got := actionFromHeader(in); got != want {
			t.Errorf("actionFromHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteSOAPResponseEscapesResult(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSOAPResponse(rec, serviceContentDirectory, "Browse", []soapArg{
		{Name: "Result", Value: `<DIDL-Lite a="1">&amp;</DIDL-Lite>`},
		{Name: "NumberReturned", Value: "1"},
		{Name: "TotalMatches", Value: "1"},
		{Name: "UpdateID", Value: "0"},
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<u:BrowseResponse") {
		t.Errorf("missing response element: %s", body)
	}
	if strings.Contains(body, `<DIDL-Lite a="1">`) {
		t.Errorf("Result was not escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;DIDL-Lite") {
		t.Errorf("expected escaped DIDL, got: %s", body)
	}
	// Argument order must be preserved for clients that match positionally.
	resultIdx := strings.Index(body, "<Result>")
	countIdx := strings.Index(body, "<NumberReturned>")
	if resultIdx < 0 || countIdx < 0 || resultIdx > countIdx {
		t.Errorf("response arguments are out of order: %s", body)
	}
}

func TestWriteSOAPFault(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSOAPFault(rec, errNoSuchObject, "No such object")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<errorCode>701</errorCode>",
		"<errorDescription>No such object</errorDescription>",
		"UPnPError",
		"s:Fault",
		nsControl,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fault is missing %q: %s", want, body)
		}
	}
}
