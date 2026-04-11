/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package grpcqueue implements TaskQueue by forwarding calls to an external
// gRPC plugin service (RFC-0025). It is registered under the scheme "grpc" and
// selected when TASK_QUEUE_URL starts with "grpc://".
package grpcqueue

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	pluginv1 "github.com/kubeswarm/kubeswarm/proto/pluginv1"
)

func init() {
	queue.RegisterQueue("grpc", func(addr string, maxRetries int) (queue.TaskQueue, error) {
		// addr is the raw URL; strip the "grpc://" scheme prefix.
		host := addr
		if len(addr) > 7 && addr[:7] == "grpc://" {
			host = addr[7:]
		}
		return newGRPCQueue(host, maxRetries)
	})
}

type grpcQueue struct {
	conn       *grpc.ClientConn
	client     pluginv1.TaskQueueClient
	maxRetries int
}

func newGRPCQueue(addr string, maxRetries int) (*grpcQueue, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, agenterrors.NewQueueError(
			agenterrors.ErrQueueTimeout,
			fmt.Sprintf("grpc queue: dial %s: %v", addr, err),
			err,
		)
	}
	return &grpcQueue{
		conn:       conn,
		client:     pluginv1.NewTaskQueueClient(conn),
		maxRetries: maxRetries,
	}, nil
}

func (q *grpcQueue) Submit(ctx context.Context, prompt string, meta map[string]string) (string, error) {
	resp, err := q.client.Submit(ctx, &pluginv1.SubmitRequest{Prompt: prompt, Meta: meta})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return "", agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue submit: %v", err),
			err,
		)
	}
	return resp.TaskId, nil
}

func (q *grpcQueue) Poll(ctx context.Context) (*queue.Task, error) {
	resp, err := q.client.Poll(ctx, &pluginv1.PollRequest{TimeoutMs: 2000})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return nil, agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue poll: %v", err),
			err,
		)
	}
	if resp.Task == nil || resp.Task.Id == "" {
		return nil, nil
	}
	return &queue.Task{
		ID:     resp.Task.Id,
		Prompt: resp.Task.Prompt,
		Meta:   resp.Task.Meta,
	}, nil
}

func (q *grpcQueue) Ack(task queue.Task, result string, usage queue.TokenUsage, _ map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := q.client.Ack(ctx, &pluginv1.AckRequest{
		Task:         toProtoTask(task),
		Result:       result,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue ack: %v", err),
			err,
		)
	}
	return nil
}

func (q *grpcQueue) Nack(task queue.Task, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := q.client.Nack(ctx, &pluginv1.NackRequest{
		Task:   toProtoTask(task),
		Reason: reason,
	})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue nack: %v", err),
			err,
		)
	}
	return nil
}

func (q *grpcQueue) Results(ctx context.Context, taskIDs []string) ([]queue.TaskResult, error) {
	resp, err := q.client.Results(ctx, &pluginv1.ResultsRequest{TaskIds: taskIDs})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return nil, agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue results: %v", err),
			err,
		)
	}
	out := make([]queue.TaskResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, queue.TaskResult{
			TaskID: r.TaskId,
			Output: r.Output,
			Error:  r.Error,
			Usage:  queue.TokenUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens},
		})
	}
	return out, nil
}

func (q *grpcQueue) Cancel(ctx context.Context, taskIDs []string) error {
	_, err := q.client.Cancel(ctx, &pluginv1.CancelRequest{TaskIds: taskIDs})
	if err != nil {
		code := agenterrors.ErrQueueFull
		if ctx.Err() != nil {
			code = agenterrors.ErrQueueTimeout
		}
		return agenterrors.NewQueueError(
			code,
			fmt.Sprintf("grpc queue cancel: %v", err),
			err,
		)
	}
	return nil
}

func (q *grpcQueue) Close() {
	_ = q.conn.Close()
}

func toProtoTask(t queue.Task) *pluginv1.Task {
	return &pluginv1.Task{Id: t.ID, Prompt: t.Prompt, Meta: t.Meta}
}
