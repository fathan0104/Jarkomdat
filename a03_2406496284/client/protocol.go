package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"a03"
)

// Grader contract: retain these aliases and all exported function signatures.
type HttpRequest = a03.HttpRequest
type HttpResponse = a03.HttpResponse

func isTokenClient(s string) bool {
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

// RequestEncoder builds the request line, headers, and terminating empty line.
func RequestEncoder(request HttpRequest) ([]byte, error) {
	if !isTokenClient(request.Method) {
		return nil, errors.New("invalid method token")
	}
	if strings.ContainsAny(request.URI, " \t\r\n") {
		return nil, errors.New("whitespace in URI")
	}
	if request.Version != "" && request.Version != a03.HTTPVersion {
		return nil, errors.New("invalid version")
	}

	headers := make(a03.Headers)
	for k, v := range request.Headers {
		if strings.ContainsAny(k, " \t\r\n:") {
			return nil, errors.New("invalid character in header name")
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("CR or LF in header value")
		}
		headers[strings.ToLower(k)] = v
	}

	hostVal, hasHost := headers["host"]
	if !hasHost || hostVal == "" {
		return nil, errors.New("missing or empty Host header")
	}
	acceptVal, hasAccept := headers["accept"]
	if !hasAccept || acceptVal == "" {
		return nil, errors.New("missing or empty Accept header")
	}

	headers["connection"] = "close"

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s %s\r\n", request.Method, request.URI, a03.HTTPVersion))
	for k, v := range headers {
		sb.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	sb.WriteString("\r\n")

	return []byte(sb.String()), nil
}

// ResponseDecoder validates a complete HTTP/1.1 response and its Content-Length.
func ResponseDecoder(message []byte) (HttpResponse, error) {
	raw := string(message)
	headerEnd := strings.Index(raw, "\r\n\r\n")
	if headerEnd == -1 {
		return HttpResponse{}, errors.New("incomplete header")
	}

	headerPart := raw[:headerEnd]
	bodyBytes := message[headerEnd+4:]

	lines := strings.Split(headerPart, "\r\n")
	if len(lines) < 1 {
		return HttpResponse{}, errors.New("empty status line")
	}

	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 || parts[0] != a03.HTTPVersion {
		return HttpResponse{}, errors.New("malformed status line")
	}
	statusCode, err := strconv.Atoi(parts[1])
	if err != nil {
		return HttpResponse{}, errors.New("invalid status code")
	}

	headers := make(a03.Headers)
	for i := 1; i < len(lines); i++ {
		colonIdx := strings.Index(lines[i], ":")
		if colonIdx != -1 {
			k := strings.TrimSpace(strings.ToLower(lines[i][:colonIdx]))
			v := strings.TrimSpace(lines[i][colonIdx+1:])
			headers[k] = v
		}
	}

	// Di dalam ResponseDecoder (client/protocol.go)
	clStr, ok := headers["content-length"]
	if !ok {
		return HttpResponse{}, errors.New("missing Content-Length")
	}
	cl, err := strconv.Atoi(clStr)
	if err != nil || cl < 0 {
		return HttpResponse{}, errors.New("invalid Content-Length")
	}

	// Validasi jumlah byte pada wire cocok dengan Content-Length
	if len(bodyBytes) != cl {
		return HttpResponse{}, errors.New("body size mismatch with Content-Length")
	}

	finalBody := bodyBytes
	if strings.ToLower(headers["content-encoding"]) == "gzip" && len(bodyBytes) > 0 {
		gzReader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
		if err != nil {
			return HttpResponse{}, fmt.Errorf("gzip error: %w", err)
		}
		defer gzReader.Close()
		decompressed, err := io.ReadAll(gzReader)
		if err != nil {
			return HttpResponse{}, fmt.Errorf("gzip decompress error: %w", err)
		}
		finalBody = decompressed
	}

	// Headers["content-length"] tetap menyimpan string `clStr` mentah!
	return HttpResponse{
		Version:    parts[0],
		StatusCode: statusCode,
		Headers:    headers,
		Body:       finalBody,
	}, nil
}

// Fetch sends one request and reads one complete response.
func Fetch(request HttpRequest, conn net.Conn) (HttpResponse, error) {
	conn.SetReadDeadline(time.Now().Add(a03.IOTimeout))

	reqBytes, err := RequestEncoder(request)
	if err != nil {
		return HttpResponse{}, err
	}

	_, err = conn.Write(reqBytes)
	if err != nil {
		return HttpResponse{}, err
	}

	var responseData []byte
	buf := make([]byte, 2048)
	headerParsed := false
	expectedBodyLen := -1
	headerLen := -1

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			responseData = append(responseData, buf[:n]...)
			if !headerParsed {
				headerEnd := bytes.Index(responseData, []byte("\r\n\r\n"))
				if headerEnd != -1 {
					headerParsed = true
					headerLen = headerEnd + 4
					headerPart := string(responseData[:headerEnd])
					lines := strings.Split(headerPart, "\r\n")
					for _, line := range lines[1:] {
						colonIdx := strings.Index(line, ":")
						if colonIdx != -1 {
							k := strings.TrimSpace(strings.ToLower(line[:colonIdx]))
							v := strings.TrimSpace(line[colonIdx+1:])
							if k == "content-length" {
								expectedBodyLen, _ = strconv.Atoi(v)
								break
							}
						}
					}
				}
			}
			if headerParsed && expectedBodyLen >= 0 {
				if len(responseData)-headerLen >= expectedBodyLen {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}

	return ResponseDecoder(responseData)
}