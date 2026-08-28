package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file bridges the KV data plane: it proxies GET/PUT/DELETE /api/v1/kv/{key} to a node's gRPC
// ClientService (the same quorum-coordinated path helixctl uses), failing over across nodes so a
// single unreachable coordinator does not fail the request. Values are arbitrary bytes, so they
// are carried losslessly as base64 and, when valid UTF-8, also as a convenience string.
//
// Note: {key} matches a single path segment, so a key containing a slash must be sent with the
// slash percent-encoded by the caller; the console does this.

type kvGetResponse struct {
	Key         string `json:"key"`
	Found       bool   `json:"found"`
	Value       string `json:"value,omitempty"`
	ValueBase64 string `json:"value_base64"`
}

type kvPutRequest struct {
	Value       *string `json:"value"`
	ValueBase64 *string `json:"value_base64"`
}

func kvValueResponse(key string, value []byte) kvGetResponse {
	resp := kvGetResponse{
		Key:         key,
		Found:       true,
		ValueBase64: base64.StdEncoding.EncodeToString(value),
	}
	if utf8.Valid(value) {
		resp.Value = string(value)
	}
	return resp
}

func decodeValue(req kvPutRequest) ([]byte, error) {
	switch {
	case req.Value != nil && req.ValueBase64 != nil:
		return nil, errors.New("provide exactly one of value or value_base64")
	case req.Value != nil:
		return []byte(*req.Value), nil
	case req.ValueBase64 != nil:
		b, err := base64.StdEncoding.DecodeString(*req.ValueBase64)
		if err != nil {
			return nil, fmt.Errorf("value_base64 is not valid base64: %w", err)
		}
		return b, nil
	default:
		return nil, errors.New("provide value or value_base64")
	}
}

// statusToHTTP maps a gRPC error from a node into an HTTP status and message. Not-found never
// reaches here (the client reports it as found=false). Quorum failures arrive as codes.Internal
// with a descriptive message, which is relayed so the console can show why.
func statusToHTTP(err error) (int, string) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusBadGateway, err.Error()
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.Unavailable:
		return http.StatusServiceUnavailable, st.Message()
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, st.Message()
	default:
		return http.StatusBadGateway, st.Message()
	}
}

func (s *Server) handleKVGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}
	if len(s.kv) == 0 {
		writeError(w, http.StatusServiceUnavailable, "no cluster nodes configured")
		return
	}
	var (
		value   []byte
		found   bool
		lastErr error
	)
	for _, c := range s.kv {
		v, f, err := c.Get(r.Context(), []byte(key))
		if err == nil {
			value, found, lastErr = v, f, nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		code, msg := statusToHTTP(lastErr)
		writeError(w, code, msg)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, kvValueResponse(key, value))
}

func (s *Server) handleKVPut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}
	var req kvPutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	value, err := decodeValue(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.kvMutate(r.Context(), func(c kvClient) error {
		return c.Put(r.Context(), []byte(key), value)
	}); err != nil {
		code, msg := statusToHTTP(err)
		writeError(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "ok": true})
}

func (s *Server) handleKVDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}
	if err := s.kvMutate(r.Context(), func(c kvClient) error {
		return c.Delete(r.Context(), []byte(key))
	}); err != nil {
		code, msg := statusToHTTP(err)
		writeError(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "deleted": true})
}

// kvClient is the subset of the gRPC client the handlers use, so the pool and failover are
// testable with a fake. *rpc.Client satisfies it.
type kvClient interface {
	Get(ctx context.Context, key []byte) (value []byte, found bool, err error)
	Put(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
	Close() error
}

// errNoNodes is returned when the gateway has no KV clients configured.
var errNoNodes = status.Error(codes.Unavailable, "no cluster nodes configured")

// kvMutate runs a write or delete against each node until one succeeds, returning the last error
// if all fail. Any node can coordinate, so this gives failover across a downed coordinator.
func (s *Server) kvMutate(_ context.Context, op func(kvClient) error) error {
	if len(s.kv) == 0 {
		return errNoNodes
	}
	var lastErr error
	for _, c := range s.kv {
		if err := op(c); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}
