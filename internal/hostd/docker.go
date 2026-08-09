package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Docker Engine API, spoken directly over its Unix socket.
//
// Not the docker/docker client library. That library is enormous, and every line
// of it would run as root in the process whose entire value is being small
// enough for one person to read and believe. Homebase uses a handful of
// endpoints; the Engine API is versioned, documented and stable.
//
// It also keeps the CI check that hostd has no third-party dependencies
// passing, and that check is not a formality — it is what makes ADR-0002's
// commitment observable rather than aspirational. See ADR-0012.

const (
	dockerSocket = "/var/run/docker.sock"

	// The API version is negotiated, not pinned, and these are the bounds.
	//
	// A pinned version was the first attempt, on the reasoning that an upgraded
	// Docker then cannot change a response shape underneath a root process. It
	// does not work: the Engine does *not* negotiate downwards. It refuses
	// anything below its own floor, and that floor rises — Docker 29 rejects
	// v1.43 outright with "minimum supported API version is 1.44". A pinned
	// client is a client that stops working when the user's Docker is upgraded,
	// on an appliance whose whole promise is that it keeps working.
	//
	// So: ask the daemon what it supports and choose within these bounds.
	//
	// dockerMaxAPIVersion is the newest version whose responses hostd has been
	// written against. dockerMinAPIVersion is the oldest it will speak; below
	// this the create-container payload differs enough to matter.
	dockerMinAPIVersion = "1.41"
	dockerMaxAPIVersion = "1.52"
)

// docker is a minimal Engine API client.
type docker struct {
	http   *http.Client
	socket string

	// The negotiated version, resolved on first use and then reused.
	//
	// Only a successful negotiation is remembered. sync.Once was the obvious
	// shape and it is wrong here: it would cache the failure too, so a hostd
	// that happened to be asked something before Docker finished starting would
	// refuse every operation for the rest of its life. On a machine that has
	// just booted, hostd starting first is the normal case.
	versionMu sync.Mutex
	version   string
}

func newDocker(socket string) *docker {
	if socket == "" {
		socket = dockerSocket
	}
	return &docker{
		socket: socket,
		http: &http.Client{
			// Pulling an image on a slow connection is the long pole. The
			// per-operation timeout in the registry bounds the whole call; this
			// exists so a wedged daemon cannot hold a connection open forever.
			Timeout: 30 * time.Minute,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// ErrDockerUnavailable means the daemon could not be reached at all.
//
// Distinct from an operation failing: the machine is fine, the container runtime
// is not running, and the honest thing to tell a user is that rather than
// implying their application is broken.
var ErrDockerUnavailable = &Error{
	Code:    "docker.unavailable",
	Message: "Homebase cannot reach the part of the system that runs applications.",
	Detail:  "the Docker daemon is not responding on " + dockerSocket,
	// Recoverable: this is usually a service that has not started yet.
	Recoverable: true,
	Recovery:    "Wait a moment and try again. If it persists, restart the server.",
	Status:      503,
}

// apiVersion returns the version prefix to use, negotiating on first call.
//
// /version is itself unversioned, which is what makes this possible: there is
// one endpoint that answers regardless of what the client would have guessed.
func (d *docker) apiVersion(ctx context.Context) (string, error) {
	d.versionMu.Lock()
	defer d.versionMu.Unlock()

	if d.version != "" {
		return d.version, nil
	}

	version, err := d.negotiate(ctx)
	if err != nil {
		return "", err
	}

	d.version = version
	return version, nil
}

func (d *docker) negotiate(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return "", err
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return "", ErrDockerUnavailable
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", ErrDockerUnavailable
	}

	var info struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
		MinAPI     string `json:"MinAPIVersion"`
	}
	if err := json.Unmarshal(payload, &info); err != nil || info.APIVersion == "" {
		return "", &Error{
			Code:        "docker.unreadable_version",
			Message:     "Homebase could not work out which version of Docker this server has.",
			Detail:      truncateDetail(string(payload)),
			Recoverable: false,
			Status:      500,
		}
	}

	chosen := info.APIVersion
	if compareAPIVersions(chosen, dockerMaxAPIVersion) > 0 {
		chosen = dockerMaxAPIVersion
	}

	// The daemon's floor wins over our ceiling. Speaking a version newer than
	// hostd was written against is a risk; refusing to run at all on a Docker
	// newer than this release is a certainty, and on an appliance the certainty
	// is worse. The endpoints used here — create, start, stop, inspect, logs,
	// pull — have been stable across every version in this range.
	if info.MinAPI != "" && compareAPIVersions(chosen, info.MinAPI) < 0 {
		chosen = info.MinAPI
	}

	if compareAPIVersions(chosen, dockerMinAPIVersion) < 0 {
		return "", &Error{
			Code:    "docker.unsupported_version",
			Message: "This server's version of Docker is too old for Homebase.",
			Detail: fmt.Sprintf("Docker %s speaks API %s at newest; Homebase needs %s or later",
				info.Version, info.APIVersion, dockerMinAPIVersion),
			Recoverable: false,
			Recovery:    "Update Docker on this server.",
			Status:      500,
		}
	}

	return "v" + chosen, nil
}

// compareAPIVersions orders two dotted Engine API versions.
//
// Not a string comparison: "1.9" sorts after "1.10" alphabetically and before it
// numerically, and getting that backwards would pick a version the daemon
// rejects.
func compareAPIVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var x, y int
		if i < len(aParts) {
			x, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			y, _ = strconv.Atoi(bParts[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func truncateDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func (d *docker) do(ctx context.Context, method, path string, body any, out any) error {
	version, err := d.apiVersion(ctx)
	if err != nil {
		return err
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method,
		"http://docker/"+version+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return ErrDockerUnavailable
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading the response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &problem)
		message := problem.Message
		if message == "" {
			message = strings.TrimSpace(string(payload))
		}
		return &dockerError{Status: resp.StatusCode, Message: message}
	}

	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decoding the response: %w", err)
		}
	}
	return nil
}

type dockerError struct {
	Status  int
	Message string
}

func (e *dockerError) Error() string {
	return fmt.Sprintf("docker: %s (status %d)", e.Message, e.Status)
}

// NotFound reports whether the daemon said the thing does not exist, which is
// frequently an ordinary answer rather than a failure.
func (e *dockerError) NotFound() bool { return e.Status == http.StatusNotFound }

func (d *docker) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return ErrDockerUnavailable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return ErrDockerUnavailable
	}
	return nil
}

// pullImage fetches an image, streaming progress to the callback.
//
// The Engine streams newline-delimited JSON while pulling. Reporting it matters
// more than it looks: pulling Jellyfin is 1.8 GB on a domestic connection, and
// a progress bar is the difference between waiting and assuming it has hung.
func (d *docker) pullImage(ctx context.Context, reference string, progress func(status string)) error {
	// A reference is split into name and tag by the API rather than passed
	// whole, so nothing in it can be read as another query parameter.
	version, err := d.apiVersion(ctx)
	if err != nil {
		return err
	}

	name, tag := splitReference(reference)

	query := url.Values{}
	query.Set("fromImage", name)
	if tag != "" {
		query.Set("tag", tag)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://docker/"+version+"/images/create?"+query.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return ErrDockerUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &problem)
		return &dockerError{Status: resp.StatusCode, Message: problem.Message}
	}

	// The stream returns 200 and then reports failure in the body, so the status
	// code alone is not evidence the pull worked.
	decoder := json.NewDecoder(resp.Body)
	for {
		var event struct {
			Status         string `json:"status"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("reading the pull stream: %w", err)
		}

		if event.Error != "" {
			return &dockerError{Status: 500, Message: event.Error}
		}
		if progress != nil && event.Status != "" {
			if event.ProgressDetail.Total > 0 {
				progress(fmt.Sprintf("%s (%s of %s)", event.Status,
					humanBytes(event.ProgressDetail.Current),
					humanBytes(event.ProgressDetail.Total)))
			} else {
				progress(event.Status)
			}
		}
	}

	return nil
}

// containerConfig is the create request.
//
// Constructed by hostd from a manifest it read itself. Nothing here comes from
// core — that is the whole point of ADR-0012, and the reason this type is not
// exported.
type containerConfig struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   hostConfig          `json:"HostConfig"`
}

type hostConfig struct {
	Binds          []string                 `json:"Binds,omitempty"`
	PortBindings   map[string][]portBinding `json:"PortBindings,omitempty"`
	RestartPolicy  restartPolicy            `json:"RestartPolicy"`
	Memory         int64                    `json:"Memory,omitempty"`
	CpuShares      int                      `json:"CpuShares,omitempty"`
	CapDrop        []string                 `json:"CapDrop,omitempty"`
	CapAdd         []string                 `json:"CapAdd,omitempty"`
	SecurityOpt    []string                 `json:"SecurityOpt,omitempty"`
	ReadonlyRootfs bool                     `json:"ReadonlyRootfs,omitempty"`
	NetworkMode    string                   `json:"NetworkMode,omitempty"`
	Devices        []deviceMapping          `json:"Devices,omitempty"`
	// Privileged is deliberately present and deliberately never set. Its absence
	// from the struct would make it impossible to assert that it is false.
	Privileged bool `json:"Privileged"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type deviceMapping struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

type createResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// hasImage reports whether an image is already on this machine.
//
// This is what makes an install possible with the internet unplugged. A home
// server whose broadband is down, or which is behind a captive portal, or whose
// DNS has stopped answering, should still be able to start an application whose
// bytes are already on its disk.
func (d *docker) hasImage(ctx context.Context, reference string) bool {
	var out struct {
		ID string `json:"Id"`
	}
	err := d.do(ctx, http.MethodGet, "/images/"+url.PathEscape(reference)+"/json", nil, &out)
	return err == nil && out.ID != ""
}

func (d *docker) createContainer(ctx context.Context, name string, config containerConfig) (string, error) {
	query := url.Values{}
	query.Set("name", name)

	var response createResponse
	if err := d.do(ctx, http.MethodPost,
		"/containers/create?"+query.Encode(), config, &response); err != nil {
		return "", err
	}
	return response.ID, nil
}

func (d *docker) startContainer(ctx context.Context, name string) error {
	return d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil, nil)
}

// stopContainer asks the container to stop, giving it time to shut down.
//
// The grace period is not politeness: an application writing a file when it is
// killed leaves a partial file, and some of those files are somebody's library
// database.
func (d *docker) stopContainer(ctx context.Context, name string, graceSeconds int) error {
	query := url.Values{}
	query.Set("t", fmt.Sprint(graceSeconds))
	return d.do(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(name)+"/stop?"+query.Encode(), nil, nil)
}

func (d *docker) restartContainer(ctx context.Context, name string, graceSeconds int) error {
	query := url.Values{}
	query.Set("t", fmt.Sprint(graceSeconds))
	return d.do(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(name)+"/restart?"+query.Encode(), nil, nil)
}

// removeContainer deletes the container. Volumes are never removed with it:
// application data outlives its container, and uninstalling is not a decision to
// delete data. See docs/architecture/data-layout.md.
func (d *docker) removeContainer(ctx context.Context, name string, force bool) error {
	query := url.Values{}
	query.Set("v", "false")
	if force {
		query.Set("force", "true")
	}
	err := d.do(ctx, http.MethodDelete,
		"/containers/"+url.PathEscape(name)+"?"+query.Encode(), nil, nil)

	// Already gone is the desired state, so removing twice must succeed.
	var dockerErr *dockerError
	if err != nil && asDockerError(err, &dockerErr) && dockerErr.NotFound() {
		return nil
	}
	return err
}

type containerState struct {
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// inspectContainer returns the container's state, or (nil, nil) when there is no
// such container — which is an ordinary answer for an application that is not
// installed.
func (d *docker) inspectContainer(ctx context.Context, name string) (*containerState, error) {
	var state containerState
	err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &state)
	if err != nil {
		var dockerErr *dockerError
		if asDockerError(err, &dockerErr) && dockerErr.NotFound() {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

// containerLogs returns the last n lines, combined stdout and stderr.
func (d *docker) containerLogs(ctx context.Context, name string, lines int) (string, error) {
	version, err := d.apiVersion(ctx)
	if err != nil {
		return "", err
	}

	query := url.Values{}
	query.Set("stdout", "true")
	query.Set("stderr", "true")
	query.Set("tail", fmt.Sprint(lines))
	query.Set("timestamps", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://docker/"+version+"/containers/"+url.PathEscape(name)+
			"/logs?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return "", ErrDockerUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", &dockerError{Status: resp.StatusCode, Message: "could not read logs"}
	}

	// Docker multiplexes stdout and stderr into a framed stream when the
	// container has no TTY: an 8-byte header per chunk, with the payload length
	// in the last four bytes. Returning it raw would give the reader control
	// characters interleaved with their log lines.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return demultiplexDockerStream(body), nil
}

// demultiplexDockerStream strips Docker's stream framing.
func demultiplexDockerStream(body []byte) string {
	var out strings.Builder
	for len(body) >= 8 {
		// A header always begins with a stream type of 0, 1 or 2. Anything else
		// means this stream was not framed, so return it as it came.
		if body[0] > 2 {
			return string(body)
		}
		size := int(body[4])<<24 | int(body[5])<<16 | int(body[6])<<8 | int(body[7])
		body = body[8:]
		if size > len(body) {
			size = len(body)
		}
		out.Write(body[:size])
		body = body[size:]
	}
	if len(body) > 0 && out.Len() == 0 {
		return string(body)
	}
	return out.String()
}

func asDockerError(err error, target **dockerError) bool {
	e, ok := err.(*dockerError)
	if ok {
		*target = e
	}
	return ok
}

// splitReference separates an image reference into a name and a tag or digest.
//
// The colon in a registry's port (registry:5000/image) is not a tag separator,
// so the split looks only after the last slash.
func splitReference(reference string) (name, tag string) {
	if at := strings.LastIndex(reference, "@"); at != -1 {
		return reference[:at], reference[at+1:]
	}

	slash := strings.LastIndex(reference, "/")
	colon := strings.LastIndex(reference, ":")
	if colon > slash {
		return reference[:colon], reference[colon+1:]
	}
	return reference, ""
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"kB", "MB", "GB", "TB"}
	value := float64(n)
	for _, suffix := range units {
		value /= unit
		if value < unit {
			if value >= 10 {
				return fmt.Sprintf("%.0f %s", value, suffix)
			}
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}
