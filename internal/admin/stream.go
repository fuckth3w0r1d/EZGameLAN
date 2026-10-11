package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ServeStream reads one bounded JSON request and writes one response.
func ServeStream(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	controller *Controller,
) error {
	if controller == nil {
		return errors.New("admin controller is nil")
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxRequestSize+1))
	if err != nil {
		return fmt.Errorf("read management request: %w", err)
	}
	if len(data) > MaxRequestSize {
		return encodeResponse(output, failure("E_CONFIG_REQUEST", "管理请求超过大小限制"))
	}
	request, err := decodeRequest(data)
	if err != nil {
		return encodeResponse(output, failure("E_CONFIG_REQUEST", "管理请求格式错误"))
	}
	return encodeResponse(output, controller.Execute(ctx, request))
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
