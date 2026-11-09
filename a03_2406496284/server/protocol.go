package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"a03"
)

// Grader contract: retain these aliases and all exported function signatures.
type HttpRequest = a03.HttpRequest
type HttpResponse = a03.HttpResponse
type Student = a03.Student
type GreetResponse = a03.GreetResponse

// ServerStudent is configured before serving. Tests may replace it between cases.
// Do not change it while handling a connection.
var ServerStudent Student

func isToken(s string) bool {
	if s == "" {
		return false
	}
	const tchars = "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(tchars, rune(s[i])) {
			return false
		}
	}
	return true
}

// RequestDecoder (TODO D)
func RequestDecoder(message []byte) (HttpRequest, error) {
	if len(message) > a03.MaxRequestHeaderBytes {
		return HttpRequest{}, errors.New("header size exceeds limit")
	}

	raw := string(message)
	headerEnd := strings.Index(raw, "\r\n\r\n")
	if headerEnd == -1 {
		return HttpRequest{}, errors.New("missing header terminator CRLF CRLF")
	}

	headerPart := raw[:headerEnd]
	lines := strings.Split(headerPart, "\r\n")
	if len(lines) < 1 || lines[0] == "" {
		return HttpRequest{}, errors.New("empty request line")
	}

	for _, line := range lines {
		if strings.Contains(line, "\n") || strings.Contains(line, "\r") {
			return HttpRequest{}, errors.New("malformed CRLF in headers")
		}
	}

	parts := strings.Split(lines[0], " ")
	if len(parts) != 3 {
		return HttpRequest{}, errors.New("malformed request line")
	}

	method, uri, version := parts[0], parts[1], parts[2]
	if version != a03.HTTPVersion {
		return HttpRequest{}, errors.New("unsupported version")
	}

	headers := make(a03.Headers)
	for i := 1; i < len(lines); i++ {
		colonIdx := strings.Index(lines[i], ":")
		if colonIdx == -1 {
			return HttpRequest{}, errors.New("header line missing colon")
		}
		k := strings.TrimSpace(strings.ToLower(lines[i][:colonIdx]))
		v := strings.TrimSpace(lines[i][colonIdx+1:])
		headers[k] = v
	}

	hostVal, ok := headers["host"]
	if !ok || hostVal == "" {
		return HttpRequest{}, errors.New("missing or empty Host header")
	}

	return HttpRequest{
		Method:  method,
		URI:     uri,
		Version: a03.HTTPVersion,
		Headers: headers,
	}, nil
}

// HandleRequest (TODO E, F, G)
func HandleRequest(request HttpRequest) HttpResponse {
	if !isToken(request.Method) || (request.Version != "" && request.Version != a03.HTTPVersion) || request.Headers["host"] == "" {
		return makeErrorResponse(400, nil)
	}

	if request.Method != "GET" {
		return makeErrorResponse(405, a03.Headers{"allow": "GET"})
	}

	u, err := url.Parse(request.URI)
	if err != nil {
		return makeErrorResponse(400, nil)
	}
	path := u.Path

	if path == "/" {
		bodyText := fmt.Sprintf("Halo, dunia! Aku %s sedang mengerjakan A03", ServerStudent.Nama)
		return HttpResponse{
			Version:    a03.HTTPVersion,
			StatusCode: 200,
			Headers: a03.Headers{
				"content-type": "text/html",
				"connection":   "close",
			},
			Body: []byte(bodyText),
		}
	}

	if strings.HasPrefix(path, "/greet/") {
		npmTarget := strings.TrimPrefix(path, "/greet/")
		if npmTarget != ServerStudent.Npm || len(npmTarget) != 10 {
			return makeErrorResponse(404, nil)
		}

		greeter := ServerStudent.Nama
		if val := u.Query().Get("name"); val != "" {
			greeter = val
		}

		chosenType, ok := negotiateAccept(request.Headers["accept"])
		if !ok {
			return makeErrorResponse(406, nil)
		}

		payload := GreetResponse{
			Student: ServerStudent,
			Greeter: greeter,
		}

		var bodyBytes []byte
		if chosenType == "application/json" {
			bodyBytes, _ = json.Marshal(payload)
		} else {
			bodyBytes, _ = xml.Marshal(payload)
		}

		headers := a03.Headers{
			"content-type": chosenType,
			"connection":   "close",
		}

		// HandleRequest hanya menyetel header "content-encoding": "gzip", 
		// proses kompresi body dilakukan saat ResponseEncoder!
		if supportsGzip(request.Headers["accept-encoding"]) {
			headers["content-encoding"] = "gzip"
		}

		return HttpResponse{
			Version:    a03.HTTPVersion,
			StatusCode: 200,
			Headers:    headers,
			Body:       bodyBytes,
		}
	}

	return makeErrorResponse(404, nil)
}

// ResponseEncoder (TODO H)
func ResponseEncoder(response HttpResponse) ([]byte, error) {
	if response.Version != "" && response.Version != a03.HTTPVersion {
		return nil, errors.New("invalid version")
	}

	headers := make(a03.Headers)
	for k, v := range response.Headers {
		headers[strings.ToLower(k)] = v
	}

	if headers["content-type"] == "" {
		return nil, errors.New("missing Content-Type header")
	}

	reason := statusText(response.StatusCode)
	if reason == "" {
		return nil, errors.New("unsupported status code")
	}

	body := response.Body

	// Kompresi gzip dilakukan di sini jika status 200 dan content-encoding == gzip
	if response.StatusCode == 200 && headers["content-encoding"] == "gzip" {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		gz.Write(body)
		gz.Close()
		body = b.Bytes()
	} else {
		delete(headers, "content-encoding")
	}

	headers["connection"] = "close"
	headers["content-length"] = strconv.Itoa(len(body))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %d %s\r\n", a03.HTTPVersion, response.StatusCode, reason))
	for k, v := range headers {
		sb.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	sb.WriteString("\r\n")

	resBytes := []byte(sb.String())
	resBytes = append(resBytes, body...)
	return resBytes, nil
}

// HandleConnection (TODO I)
func HandleConnection(conn net.Conn) error {
	// Wajib set read deadline di awal agar tidak menggantung (stalled peer)
	if err := conn.SetDeadline(time.Now().Add(a03.IOTimeout)); err != nil {
		return err
	}

	buf := make([]byte, a03.MaxRequestHeaderBytes)
	nTotal := 0

	for {
		if nTotal >= a03.MaxRequestHeaderBytes {
			resBytes, _ := ResponseEncoder(makeErrorResponse(400, nil))
			_, _ = conn.Write(resBytes)
			return errors.New("oversized header")
		}

		n, err := conn.Read(buf[nTotal:])
		if n > 0 {
			nTotal += n
			if bytes.Contains(buf[:nTotal], []byte("\r\n\r\n")) {
				break
			}
		}
		if err != nil {
			return err
		}
	}

	req, err := RequestDecoder(buf[:nTotal])
	var res HttpResponse
	if err != nil {
		res = makeErrorResponse(400, nil)
	} else {
		res = HandleRequest(req)
	}

	resBytes, err := ResponseEncoder(res)
	if err != nil {
		return err
	}
	_, err = conn.Write(resBytes)
	return err
}

func makeErrorResponse(code int, extraHeaders a03.Headers) HttpResponse {
	h := a03.Headers{
		"content-type": "text/plain",
		"connection":   "close",
	}
	for k, v := range extraHeaders {
		h[strings.ToLower(k)] = v
	}
	return HttpResponse{
		Version:    a03.HTTPVersion,
		StatusCode: code,
		Headers:    h,
		Body:       []byte{},
	}
}

func statusText(code int) string {
	switch code {
	case 200: return "OK"
	case 400: return "Bad Request"
	case 404: return "Not Found"
	case 405: return "Method Not Allowed"
	case 406: return "Not Acceptable"
	default: return ""
	}
}

func negotiateAccept(accept string) (string, bool) {
	if accept == "" || accept == "*/*" {
		return "application/json", true
	}

	jsonQ, xmlQ := -1.0, -1.0
	parts := strings.Split(accept, ",")
	for _, p := range parts {
		item := strings.TrimSpace(p)
		mediaType := item
		qVal := 1.0

		if idx := strings.Index(item, ";q="); idx != -1 {
			mediaType = strings.TrimSpace(item[:idx])
			qStr := strings.TrimSpace(item[idx+3:])
			if q, err := strconv.ParseFloat(qStr, 64); err == nil {
				qVal = q
			}
		}

		if mediaType == "application/json" {
			jsonQ = qVal
		} else if mediaType == "application/xml" {
			xmlQ = qVal
		}
	}

	if jsonQ <= 0 && xmlQ <= 0 {
		return "", false
	}
	if jsonQ >= xmlQ && jsonQ > 0 {
		return "application/json", true
	}
	if xmlQ > jsonQ && xmlQ > 0 {
		return "application/xml", true
	}
	return "", false
}

func supportsGzip(acceptEncoding string) bool {
	if acceptEncoding == "" {
		return false
	}
	parts := strings.Split(acceptEncoding, ",")
	for _, p := range parts {
		if strings.TrimSpace(strings.ToLower(p)) == "gzip" {
			return true
		}
	}
	return false
}