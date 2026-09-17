package upnp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"nanodlna/internal/didl"
)

// UPnP service types.
const (
	serviceContentDirectory  = "urn:schemas-upnp-org:service:ContentDirectory:1"
	serviceConnectionManager = "urn:schemas-upnp-org:service:ConnectionManager:1"
)

// SOAP and UPnP control namespaces.
const (
	nsSOAPEnvelope = "http://schemas.xmlsoap.org/soap/envelope/"
	nsSOAPEncoding = "http://schemas.xmlsoap.org/soap/encoding/"
	nsControl      = "urn:schemas-upnp-org:control-1-0"
)

// UPnP control error codes.
const (
	errInvalidAction = 401
	errInvalidArgs   = 402
	errActionFailed  = 501
	errNoSuchObject  = 701
)

// maxSOAPBody bounds the size of a control request we are willing to read.
const maxSOAPBody = 1 << 20

var errMalformedSOAP = errors.New("malformed SOAP request")

// soapRequest is a decoded UPnP control request.
type soapRequest struct {
	Service string
	Action  string
	Args    map[string]string
}

// soapArg is one output argument of a control response. Order is preserved
// because some clients match response arguments positionally.
type soapArg struct {
	Name  string
	Value string
}

// parseSOAPRequest decodes a UPnP control request body.
//
// The action name is taken from the SOAPACTION header when present because it
// is unambiguous, and from the body element otherwise.
func parseSOAPRequest(r *http.Request, service string) (*soapRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxSOAPBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedSOAP, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty body", errMalformedSOAP)
	}

	var env struct {
		Body struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedSOAP, err)
	}

	req := &soapRequest{Service: service, Args: map[string]string{}}

	dec := xml.NewDecoder(bytes.NewReader(env.Body.Inner))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errMalformedSOAP, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		req.Action = start.Name.Local
		if err := decodeSOAPArgs(dec, start, req.Args); err != nil {
			return nil, err
		}
		break
	}

	if action := actionFromHeader(r.Header.Get("SOAPACTION")); action != "" {
		req.Action = action
	}
	if req.Action == "" {
		return nil, fmt.Errorf("%w: no action element", errMalformedSOAP)
	}
	return req, nil
}

// decodeSOAPArgs reads the child elements of the action element into args.
func decodeSOAPArgs(dec *xml.Decoder, action xml.StartElement, args map[string]string) error {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", errMalformedSOAP, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var value string
			if err := dec.DecodeElement(&value, &t); err != nil {
				return fmt.Errorf("%w: %v", errMalformedSOAP, err)
			}
			args[t.Name.Local] = strings.TrimSpace(value)
		case xml.EndElement:
			if t.Name == action.Name {
				return nil
			}
		}
	}
}

// actionFromHeader extracts the action name from a SOAPACTION header such as
// "urn:schemas-upnp-org:service:ContentDirectory:1#Browse".
func actionFromHeader(h string) string {
	h = strings.TrimSpace(strings.Trim(h, `"`))
	if h == "" {
		return ""
	}
	if i := strings.LastIndex(h, "#"); i >= 0 {
		return strings.TrimSpace(h[i+1:])
	}
	return h
}

// writeSOAPResponse writes a successful control response.
func writeSOAPResponse(w http.ResponseWriter, service, action string, args []soapArg) {
	var b strings.Builder
	b.Grow(512)
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="` + nsSOAPEnvelope + `" s:encodingStyle="` + nsSOAPEncoding + `"><s:Body>`)
	b.WriteString("<u:" + action + `Response xmlns:u="` + service + `">`)
	for _, a := range args {
		b.WriteString("<" + a.Name + ">" + didl.EscapeText(a.Value) + "</" + a.Name + ">")
	}
	b.WriteString("</u:" + action + "Response></s:Body></s:Envelope>")

	body := []byte(b.String())
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeSOAPFault writes a UPnP control error with the conventional HTTP 500
// status.
func writeSOAPFault(w http.ResponseWriter, code int, description string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="` + nsSOAPEnvelope + `" s:encodingStyle="` + nsSOAPEncoding + `"><s:Body><s:Fault>`)
	b.WriteString(`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`)
	b.WriteString(`<UPnPError xmlns="` + nsControl + `">`)
	b.WriteString("<errorCode>" + strconv.Itoa(code) + "</errorCode>")
	b.WriteString("<errorDescription>" + didl.EscapeText(description) + "</errorDescription>")
	b.WriteString(`</UPnPError></detail></s:Fault></s:Body></s:Envelope>`)

	body := []byte(b.String())
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(body)
}
