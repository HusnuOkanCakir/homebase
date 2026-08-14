// Package hostclient talks to hostd over its Unix socket.
//
// This is the only thing in core permitted to open that socket. Everything else
// that needs a privileged operation goes through here, so that the complete set
// of privileged calls core can make is the set of methods on this type.
package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const DefaultSocket = "/run/homebase/hostd.sock"

// Error is a failure reported by hostd, in the shape of
// schemas/error.schema.json. It travels out through core's API unchanged: a
// failure that started in hostd should reach the user with the same code and
// the same explanation, not be reshaped on the way.
type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Recovery    string `json:"recovery,omitempty"`
	RequestID   string `json:"request_id,omitempty"`

	// Status is the HTTP status hostd returned, so core can choose a sensible
	// one of its own rather than flattening everything to 500.
	Status int `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrUnavailable means hostd could not be reached at all.
//
// Distinct from an operation failing: the machine is fine, the privileged
// service is not running, and the honest thing to tell a user is that Homebase
// is starting up or broken — not that their disk is missing.
var ErrUnavailable = errors.New("hostd is not reachable")

type Client struct {
	http   *http.Client
	socket string
}

func New(socket string) *Client {
	if socket == "" {
		socket = DefaultSocket
	}
	return &Client{
		socket: socket,
		http: &http.Client{
			// Generous: the timeout that matters is the per-operation one hostd
			// enforces itself. This exists so a wedged socket cannot hold a
			// request open forever.
			Timeout: 6 * time.Minute,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
				MaxIdleConns:    4,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// Call invokes a named operation.
//
// Confirmed carries the caller's assertion that the user agreed to this. core is
// where the user is, so core is where the asking happens; hostd enforces that
// the claim was made and audits it.
func (c *Client) Call(ctx context.Context, operation string, params any, confirmed bool, out any) error {
	body := []byte("{}")
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encoding parameters for %s: %w", operation, err)
		}
		body = encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://hostd/v1/op/"+operation, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if confirmed {
		req.Header.Set("X-Homebase-Confirmed", "true")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading the response to %s: %w", operation, err)
	}

	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error Error `json:"error"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error.Code == "" {
			return &Error{
				Code:    "hostd.unreadable_response",
				Message: "The host service returned something unexpected.",
				Detail:  fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(payload), 200)),
				Status:  resp.StatusCode,
			}
		}
		envelope.Error.Status = resp.StatusCode
		return &envelope.Error
	}

	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decoding the response to %s: %w", operation, err)
		}
	}
	return nil
}

// Healthy reports whether hostd is reachable and serving.
func (c *Client) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hostd/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

// Operations returns the privileged operations this build of hostd exposes.
//
// core does not need this to function — it calls operations by name. It is here
// because the list is what a reviewer, a diagnostic bundle, and eventually the
// Stage 2 policy engine need in order to know what is actually possible on this
// machine, rather than what the documentation claims.
func (c *Client) Operations(ctx context.Context) ([]Operation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hostd/v1/operations", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	var doc struct {
		Operations []Operation `json:"operations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.Operations, nil
}

type Operation struct {
	Name           string   `json:"name"`
	Summary        string   `json:"summary"`
	Risk           string   `json:"risk"`
	Permissions    []string `json:"permissions"`
	Confirmation   string   `json:"confirmation"`
	TimeoutSeconds float64  `json:"timeout_seconds"`
	Rollback       *string  `json:"rollback"`
}

// --- Typed wrappers ----------------------------------------------------------
//
// One method per operation rather than exposing Call directly. The point is that
// `git grep 'hostclient\.'` lists every privileged thing core can do.

type SystemInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Architecture  string `json:"architecture"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	CPU           struct {
		Model   string `json:"model"`
		Cores   int    `json:"cores"`
		Threads int    `json:"threads"`
	} `json:"cpu"`
	Virtualised bool `json:"virtualised"`
}

type SystemResources struct {
	Memory struct {
		TotalBytes     uint64 `json:"total_bytes"`
		AvailableBytes uint64 `json:"available_bytes"`
	} `json:"memory"`
	LoadAverage   [3]float64 `json:"load_average"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	Power         struct {
		OnBattery      *bool `json:"on_battery"`
		BatteryPercent *int  `json:"battery_percent"`
	} `json:"power"`

	// Temperature, with a nil reading meaning "this machine cannot tell" rather
	// than "cold". Every VM is in that state and so is some real hardware.
	Temperature struct {
		Celsius *int   `json:"celsius"`
		Sensor  string `json:"sensor,omitempty"`
		State   string `json:"state,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"temperature"`
}

func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := c.Call(ctx, "system.get_info", nil, false, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) SystemResources(ctx context.Context) (*SystemResources, error) {
	var res SystemResources
	if err := c.Call(ctx, "system.get_resources", nil, false, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Reboot restarts the machine.
//
// confirm must be the hostname: hostd requires the target to be named so a
// confirmation cannot be replayed against a different server.
//
// A successful return means the reboot was *accepted*, not that it finished.
// Nothing can observe it finishing — the connection dies with the machine. See
// the job system for how that is resolved afterwards.
// Rename changes what the machine calls itself.
func (c *Client) Rename(ctx context.Context, name string) (RenameResult, error) {
	params := struct {
		Name string `json:"name"`
	}{Name: name}

	var result RenameResult
	if err := c.Call(ctx, "system.rename", params, true, &result); err != nil {
		return RenameResult{}, err
	}
	return result, nil
}

// RenameResult is what the machine says it is called afterwards.
type RenameResult struct {
	Previous string `json:"previous"`
	Name     string `json:"name"`
	Message  string `json:"message"`
}

func (c *Client) Reboot(ctx context.Context, confirm, reason string) error {
	params := struct {
		Confirm string `json:"confirm"`
		Reason  string `json:"reason,omitempty"`
	}{Confirm: confirm, Reason: reason}

	err := c.Call(ctx, "system.reboot", params, true, nil)
	// The machine may go down before the response arrives, which is success
	// rather than failure. Distinguishing the two is impossible from here, so
	// the job system settles it on the next boot instead of guessing now.
	if errors.Is(err, ErrUnavailable) {
		return nil
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// NetworkInterface is one way the server is attached to a network.
type NetworkInterface struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Up        bool     `json:"up"`
	Addresses []string `json:"addresses,omitempty"`
	MAC       string   `json:"mac,omitempty"`
}

// NetworkStatus is how the server is connected, and whether it is.
type NetworkStatus struct {
	Hostname  string `json:"hostname"`
	MDNSName  string `json:"mdns_name"`
	MDNSWorks bool   `json:"mdns_works"`

	Interfaces  []NetworkInterface `json:"interfaces"`
	Gateway     string             `json:"gateway,omitempty"`
	Nameservers []string           `json:"nameservers,omitempty"`

	// Online and Reachable are separate on purpose. A server with an address on
	// a network whose broadband is down is a different problem from a server
	// with no address, and they look identical from a browser that will not
	// load.
	Online    bool `json:"online"`
	Reachable bool `json:"reachable"`
}

func (c *Client) NetworkStatus(ctx context.Context) (*NetworkStatus, error) {
	var status NetworkStatus
	if err := c.Call(ctx, "network.status", nil, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// Component is one of the packages an installation is made of.
type Component struct {
	Package string `json:"package"`
	Version string `json:"version"`
	State   string `json:"state"`
}

// UpdateStatus is what this machine is running, and where it updates from.
type UpdateStatus struct {
	Version string `json:"version"`

	// Consistent and Interrupted are the two ways a half-applied update shows
	// up, and they are separate because they are found differently: components
	// disagreeing about their version, and dpkg leaving work unfinished. A
	// machine can be either without being the other.
	Consistent  bool `json:"consistent"`
	Interrupted bool `json:"interrupted"`

	Components []Component `json:"components"`

	Channel string `json:"channel"`
	Origin  string `json:"origin"`
}

func (c *Client) UpdateStatus(ctx context.Context) (*UpdateStatus, error) {
	var status UpdateStatus
	if err := c.Call(ctx, "update.status", nil, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// UpdateCheckResult is what the archive offers, and whether it answered.
type UpdateCheckResult struct {
	Current   string `json:"current"`
	Available string `json:"available"`

	UpdateAvailable bool   `json:"update_available"`
	Channel         string `json:"channel"`
	Reachable       bool   `json:"reachable"`
	Detail          string `json:"detail,omitempty"`
}

func (c *Client) UpdateCheck(ctx context.Context) (*UpdateCheckResult, error) {
	var result UpdateCheckResult
	if err := c.Call(ctx, "update.check", nil, false, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateChannel is where a machine gets updates from.
type UpdateChannel struct {
	Channel   string `json:"channel"`
	Origin    string `json:"origin"`
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail,omitempty"`
}

func (c *Client) ConfigureUpdates(ctx context.Context, channel string) (*UpdateChannel, error) {
	var result UpdateChannel
	// Confirmed here rather than asking the caller for a word to type. The
	// dashboard's own warning is the confirmation, and hostd's requirement
	// exists so that nothing reaches this operation by accident.
	if err := c.Call(ctx, "update.configure",
		map[string]string{"channel": channel}, true, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateStarted is the answer to asking for an update, which is not the answer
// to whether it worked: by the time that is known, hostd will have restarted.
type UpdateStarted struct {
	Started bool `json:"started"`
}

func (c *Client) ApplyUpdate(ctx context.Context) (*UpdateStarted, error) {
	var result UpdateStarted
	if err := c.Call(ctx, "update.apply", nil, true, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateProgress is how far an update got, and how it ended.
type UpdateProgress struct {
	Stage   string `json:"stage"`
	Result  string `json:"result,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Running bool   `json:"running"`
}

func (c *Client) UpdateProgress(ctx context.Context) (*UpdateProgress, error) {
	var progress UpdateProgress
	if err := c.Call(ctx, "update.progress", nil, false, &progress); err != nil {
		return nil, err
	}
	return &progress, nil
}
