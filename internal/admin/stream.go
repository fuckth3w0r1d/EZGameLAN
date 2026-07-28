package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxRequestsPerStream = 128

// ServeStream processes bounded NDJSON requests. SSH callers currently send
// one request per process; accepting several frames keeps the protocol usable
// for a future persistent SSH session without adding a daemon.
func ServeStream(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	controller *Controller,
) error {
	if controller == nil {
		return errors.New("admin controller is nil")
	}
	scanner := bufio.NewScanner(io.LimitReader(input, int64(MaxRequestSize*maxRequestsPerStream)))
	scanner.Buffer(make([]byte, 1024), MaxRequestSize)
	for count := 0; scanner.Scan(); count++ {
		if count >= maxRequestsPerStream {
			return errors.New("too many management requests")
		}
		request, err := decodeRequest(scanner.Bytes())
		response := Response{}
		if err != nil {
			response = failure("E_CONFIG_REQUEST", "管理请求格式错误")
		} else {
			response = controller.Execute(ctx, request)
		}
		if err := encodeResponse(output, response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read management request: %w", err)
	}
	return nil
}

func decodeRequest(data []byte) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("trailing JSON value")
		}
		return Request{}, err
	}
	return request, nil
}

func encodeResponse(output io.Writer, response Response) error {
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(response); err != nil {
		return fmt.Errorf("encode management response: %w", err)
	}
	if buffer.Len() > MaxResponseSize {
		return errors.New("management response exceeds size limit")
	}
	if _, err := output.Write(buffer.Bytes()); err != nil {
		return fmt.Errorf("write management response: %w", err)
	}
	return nil
}
