//go:build linux || darwin

package actions

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const maxHelperMessageBytes = 64 << 10

type unixHelperClient struct {
	socketPath string
	timeout    time.Duration
}

func (c unixHelperClient) Call(operation helperOperation, request *PowerRequest, receipt *Receipt) (helperResponse, error) {
	if c.timeout <= 0 {
		c.timeout = 5 * time.Second
	}
	connection, err := net.DialTimeout("unix", c.socketPath, c.timeout)
	if err != nil {
		return helperResponse{}, fmt.Errorf("connect DoneThen power helper: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(c.timeout))
	payload := helperRequest{SchemaVersion: helperProtocolVersion, Operation: operation, Request: request, Receipt: receipt}
	if err := json.NewEncoder(connection).Encode(payload); err != nil {
		return helperResponse{}, fmt.Errorf("write power helper request: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxHelperMessageBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return helperResponse{}, fmt.Errorf("read power helper response: %w", err)
	}
	if len(line) > maxHelperMessageBytes {
		return helperResponse{}, errors.New("power helper response is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	var response helperResponse
	if err := decoder.Decode(&response); err != nil {
		return helperResponse{}, fmt.Errorf("decode power helper response: %w", err)
	}
	if response.SchemaVersion != helperProtocolVersion {
		return helperResponse{}, fmt.Errorf("unsupported power helper protocol %d", response.SchemaVersion)
	}
	if !response.OK {
		switch response.ErrorCode {
		case "no_action_in_progress":
			return response, ErrNoShutdownInProgress
		case "platform_unsupported":
			return response, ErrPlatformUnsupported
		case "privilege_unavailable", "peer_rejected", "policy_rejected":
			return response, ErrPrivilegeUnavailable
		case "active_job_conflict":
			return response, ErrPowerActionConflict
		default:
			return response, fmt.Errorf("power helper rejected %s: %s", operation, safeHelperMessage(response.Message))
		}
	}
	return response, nil
}

func safeHelperMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
