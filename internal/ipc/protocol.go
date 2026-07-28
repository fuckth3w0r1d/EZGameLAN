// Package ipc defines the bounded local protocol shared by the Windows client
// and service.
package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/r3t2/ezgamelan/internal/join"
)

const (
	Version         = 2
	MaxRequestSize  = 16 << 10
	MaxResponseSize = 256 << 10
	MaxDiagnostics  = 128 << 10
)

type Action string

const (
	ActionJoin        Action = "join"
	ActionConnect     Action = "connect"
	ActionDisconnect  Action = "disconnect"
	ActionStatus      Action = "status"
	ActionDiagnostics Action = "diagnostics"
)

type ConnectionState string

const (
	StateNotConfigured ConnectionState = "not_configured"
	StateDisconnected  ConnectionState = "disconnected"
	StateConnecting    ConnectionState = "connecting"
	StateConnected     ConnectionState = "connected"
	StateStopping      ConnectionState = "stopping"
	StateError         ConnectionState = "error"
)

type Request struct {
	Version int    `json:"version"`
	Action  Action `json:"action"`
	URL     string `json:"url,omitempty"`
	Token   string `json:"token,omitempty"`
}

type Status struct {
	State       ConnectionState `json:"state"`
	VirtualIP   string          `json:"virtual_ip,omitempty"`
	AdapterName string          `json:"adapter_name,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
}

type Response struct {
	Version     int    `json:"version"`
	OK          bool   `json:"ok"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	Status      Status `json:"status"`
	Diagnostics string `json:"diagnostics,omitempty"`
}

func WriteRequest(w io.Writer, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, request, MaxRequestSize)
}

func ReadRequest(r io.Reader) (Request, error) {
	data, err := readFrame(r, MaxRequestSize)
	if err != nil {
		return Request{}, err
	}
	request, err := decodeRequest(data)
	if err != nil {
		return Request{}, err
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func WriteResponse(w io.Writer, response Response) error {
	if err := response.Validate(); err != nil {
		return err
	}
	return writeJSONFrame(w, response, MaxResponseSize)
}

func ReadResponse(r io.Reader) (Response, error) {
	data, err := readFrame(r, MaxResponseSize)
	if err != nil {
		return Response{}, err
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode IPC response: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Response{}, err
	}
	if err := response.Validate(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func (r Request) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("E_SERVICE_PROTOCOL: 协议版本 %d 不受支持", r.Version)
	}
	switch r.Action {
	case ActionJoin:
		if _, err := join.NewInvitation(r.URL, r.Token); err != nil {
			return fmt.Errorf("E_SERVICE_REQUEST: 加入参数错误: %w", err)
		}
	case ActionConnect, ActionDisconnect, ActionStatus, ActionDiagnostics:
		if r.URL != "" || r.Token != "" {
			return errors.New("E_SERVICE_REQUEST: 当前操作包含多余参数")
		}
	default:
		return errors.New("E_SERVICE_REQUEST: 未知操作")
	}
	return nil
}

func (r Response) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("E_SERVICE_PROTOCOL: 协议版本 %d 不受支持", r.Version)
	}
	switch r.Status.State {
	case StateNotConfigured, StateDisconnected, StateConnecting,
		StateConnected, StateStopping, StateError:
	default:
		return errors.New("E_SERVICE_PROTOCOL: 连接状态错误")
	}
	if r.OK {
		if r.Code != "" {
			return errors.New("E_SERVICE_PROTOCOL: 成功响应包含错误码")
		}
	} else if r.Code == "" || r.Message == "" {
		return errors.New("E_SERVICE_PROTOCOL: 错误响应缺少错误码或提示")
	}
	if len(r.Message) > 1024 || len(r.Status.VirtualIP) > 64 ||
		len(r.Status.AdapterName) > 128 || len(r.Status.ErrorCode) > 64 ||
		len(r.Code) > 64 || len(r.Diagnostics) > MaxDiagnostics {
		return errors.New("E_SERVICE_PROTOCOL: 响应字段过长")
	}
	if !r.OK && r.Diagnostics != "" {
		return errors.New("E_SERVICE_PROTOCOL: 错误响应包含诊断内容")
	}
	return nil
}

func writeJSONFrame(w io.Writer, value any, limit int) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode IPC message: %w", err)
	}
	if len(data) == 0 || len(data) > limit {
		return errors.New("IPC message exceeds size limit")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeAll(w, header[:]); err != nil {
		return fmt.Errorf("write IPC header: %w", err)
	}
	if err := writeAll(w, data); err != nil {
		return fmt.Errorf("write IPC body: %w", err)
	}
	return nil
}

func readFrame(r io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read IPC header: %w", err)
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > uint32(limit) {
		return nil, errors.New("IPC message size is invalid")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read IPC body: %w", err)
	}
	return data, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func decodeRequest(data []byte) (Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return Request{}, fmt.Errorf("decode IPC request: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return Request{}, errors.New("IPC request must be an object")
	}
	var request Request
	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return Request{}, fmt.Errorf("decode IPC field: %w", err)
		}
		name, ok := nameToken.(string)
		if !ok {
			return Request{}, errors.New("IPC field name must be a string")
		}
		if _, exists := seen[name]; exists {
			return Request{}, fmt.Errorf("duplicate IPC field %q", name)
		}
		seen[name] = struct{}{}
		switch name {
		case "version":
			err = decoder.Decode(&request.Version)
		case "action":
			err = decoder.Decode(&request.Action)
		case "url":
			err = decoder.Decode(&request.URL)
		case "token":
			err = decoder.Decode(&request.Token)
		default:
			return Request{}, fmt.Errorf("unknown IPC field %q", name)
		}
		if err != nil {
			return Request{}, fmt.Errorf("decode IPC field %q: %w", name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Request{}, fmt.Errorf("decode IPC request end: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Request{}, err
	}
	return request, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing IPC value")
	}
	return fmt.Errorf("decode trailing IPC data: %w", err)
}
