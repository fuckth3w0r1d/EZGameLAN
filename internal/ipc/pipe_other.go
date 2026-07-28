//go:build !windows

package ipc

import (
	"context"
	"errors"
)

const PipeName = "EZGameLAN-v1"

type Handler func(context.Context, Request) Response

type PipeServer struct{}

func NewPipeServer() *PipeServer {
	return &PipeServer{}
}

func (*PipeServer) Serve(context.Context, Handler) error {
	return errors.New("named pipe service is available on Windows")
}

func (*PipeServer) Close() {}

func Call(context.Context, Request) (Response, error) {
	return Response{}, errors.New("named pipe client is available on Windows")
}
