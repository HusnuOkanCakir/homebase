package hostd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The application catalogue.
//
// Manifests are files on disk, installed by Debian packages, owned by root and
// not writable by core. hostd reads them and constructs containers itself —
// core never sends a container specification. See ADR-0012.
//
// The consequence worth keeping in mind while reading this file: the set of
// containers this machine can run is exactly the set of manifests in this
// directory. Nothing at runtime can add to it.

// DefaultCatalogueDir is where packages install manifests.
const DefaultCatalogueDir = "/usr/share/homebase/apps"

// Manifest is an installable application, as described by
// schemas/app-manifest.schema.json.
//
// Decoding is strict: an unknown field is an error rather than being ignored. A
// manifest with a misspelled key is a manifest whose author intended something
// we did not read, and the safe reading of that ambiguity is to refuse.
type Manifest struct {
	ManifestVersion int    `json:"manifest_version"`
	Revision        int    `json:"revision,omitempty"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Summary         string `json:"summary,omitempty"`
	Homepage        string `json:"homepage,omitempty"`
	License         string `json:"license,omitempty"`

	Container ManifestContainer `json:"container"`
	Network   ManifestNetwork   `json:"network,omitempty"`
	Storage   []ManifestStorage `json:"storage"`
	Health    ManifestHealth    `json:"health"`

	Permissions ManifestPermissions `json:"permissions,omitempty"`
	Resources   ManifestResources   `json:"resources,omitempty"`

	Credentials []ManifestCredential `json:"credentials,omitempty"`

	// AfterInstall is what the person who installed this still has to do.
	//
	// Prose, never a template and never a command Homebase runs — the only
	// thing that reads it is a human being. It exists because for several
	// applications it is the difference between working and usable: one that is
	// running, reachable, and asking for a password nobody was given is
	// indistinguishable from one that is broken.
	AfterInstall string `json:"after_install,omitempty"`

	// Services are the supporting containers this application cannot run
	// without — a database, a cache.
	//
	// Never reachable from anywhere but the application: each joins a private
	// network of the application's own and publishes no port on any interface.
	// What the application connects to is the service's name, which is its
	// address on that network.
	Services []ManifestService `json:"services,omitempty"`

	Capabilities    []ManifestCapability `json:"capabilities,omitempty"`
	Events          []ManifestEvent      `json:"events,omitempty"`
	SensitiveFields []string             `json:"sensitive_fields,omitempty"`
	Requires        ManifestRequires     `json:"requires,omitempty"`
}

type ManifestContainer struct {
	Image       string            `json:"image"`
	Version     string            `json:"version,omitempty"`
	Digest      string            `json:"digest,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

// Reference is what to pull: the digest when the manifest pins one, otherwise
// the tag.
//
// A digest is authoritative because it names the bytes rather than a label
// somebody can move. Where a manifest gives both, the digest wins — the tag is
// then documentation.
func (c ManifestContainer) Reference() string {
	if c.Digest != "" {
		return c.Image + "@" + c.Digest
	}
	if c.Version != "" {
		return c.Image + ":" + c.Version
	}
	return c.Image
}

// ManifestService is one supporting container.
type ManifestService struct {
	// Name is what the application connects to it as — it becomes a hostname on
	// the private network, and the container is named after it.
	Name string `json:"name"`

	Image       string            `json:"image"`
	Version     string            `json:"version,omitempty"`
	Digest      string            `json:"digest,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`

	// Storage here is always private. A database on a disk somebody can unplug
	// is a database that vanishes, and unlike a media folder there is nothing
	// sensible for the application to do about its absence.
	Storage []ManifestServiceStorage `json:"storage,omitempty"`
}

type ManifestServiceStorage struct {
	ID        string `json:"id"`
	MountPath string `json:"mount_path"`
	Backup    bool   `json:"backup,omitempty"`
}

// Reference is the image to pull, pinned the same way the application's own is.
func (s ManifestService) Reference() string {
	if s.Digest != "" {
		return s.Image + "@" + s.Digest
	}
	if s.Version != "" {
		return s.Image + ":" + s.Version
	}
	return s.Image
}

// validServiceName is what a supporting container may be called. It becomes a
// hostname on the private network, so it is held to what a hostname may be.
var validServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,20}[a-z0-9]$`)

type ManifestNetwork struct {
	InternalPort int    `json:"internal_port,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
	Path         string `json:"path,omitempty"`
	HostNetwork  bool   `json:"host_network,omitempty"`

	// ReachableFrom is "server" or "network", and empty means "server".
	//
	// Everything used to be "server": bound to 127.0.0.1 on a port Docker
	// chose, on the reasoning that applications are reached through Homebase,
	// which applies authentication. Homebase has no such proxy. So an installed
	// application ran on a random loopback port that nothing reported, and could
	// not be opened from anywhere — a media server nobody could watch anything
	// on, which is a strange thing for a media server to be.
	//
	// The VM test did not notice because it asked Docker for the port and
	// connected inside the machine. It proved the container serves HTTP and
	// nothing whatever about anybody reaching it.
	//
	// "network" is therefore not a relaxation of the rule but the honest form of
	// it: an application with its own accounts publishes itself and guards
	// itself, and one without stays on loopback until there is a proxy in front
	// of it. Which of the two an application is, is a decision made per
	// application in a root-owned manifest and reviewed in a diff (ADR-0012).
	ReachableFrom string `json:"reachable_from,omitempty"`

	// HostPort is the port on the server, when published to the network.
	// Defaults to InternalPort — stated explicitly only when the container's own
	// port is one the server already uses.
	HostPort int `json:"host_port,omitempty"`

	// Reaches are other applications this one must connect to, by id.
	//
	// Homebase joins both to a network they share, and the address is the other
	// application's id. It exists because none of the obvious routes work:
	// containers on Docker's default bridge cannot resolve each other, the
	// server's .local name does not resolve inside a container, and the bridge
	// gateway is the host where the firewall drops it — correctly, since a rule
	// letting containers reach the host's ports would let every container reach
	// every one of them.
	Reaches []string `json:"reaches,omitempty"`

	Discovery []string `json:"discovery,omitempty"`
}

// PublishedPort is the port on the server an application answers on.
func (n ManifestNetwork) PublishedPort() int {
	if n.HostPort > 0 {
		return n.HostPort
	}
	return n.InternalPort
}

// PublishedToNetwork reports whether this application is reachable from other
// machines. Default-deny: anything that does not say so is on loopback.
func (n ManifestNetwork) PublishedToNetwork() bool {
	return n.ReachableFrom == "network"
}

type ManifestStorage struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	MountPath   string `json:"mount_path"`
	Access      string `json:"access,omitempty"`
	Description string `json:"description,omitempty"`
	Backup      *bool  `json:"backup,omitempty"`
}

// ReadOnly reports whether this location is mounted read-only.
func (s ManifestStorage) ReadOnly() bool { return s.Access == "read-only" }

type ManifestHealth struct {
	Type                    string `json:"type"`
	Path                    string `json:"path,omitempty"`
	ExpectedStatus          int    `json:"expected_status,omitempty"`
	IntervalSeconds         int    `json:"interval_seconds,omitempty"`
	TimeoutSeconds          int    `json:"timeout_seconds,omitempty"`
	StartPeriodSeconds      int    `json:"start_period_seconds,omitempty"`
	FailuresBeforeUnhealthy int    `json:"failures_before_unhealthy,omitempty"`
}

type ManifestPermissions struct {
	GPU          string   `json:"gpu,omitempty"`
	Devices      []string `json:"devices,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Privileged   bool     `json:"privileged,omitempty"`
	ReadOnlyRoot *bool    `json:"read_only_root,omitempty"`

	// StartsAsRoot marks an image that cannot run as an arbitrary user.
	//
	// Its entrypoint starts as root, corrects ownership of the folders it was
	// given, and drops to its own account. Every linuxserver.io image works that
	// way, and Homebase's default — a uid of its own with every capability
	// dropped — makes all three steps fail.
	//
	// The alternative was considered and rejected. Holding every application to
	// an unprivileged uid excludes most of what people want on a home server:
	// Paperless, Immich, Nextcloud, Home Assistant and the whole Sonarr family.
	// So this exists, is declared per application, and requires a written reason
	// a reviewer can check against the image's own entrypoint.
	StartsAsRoot *ManifestElevation `json:"starts_as_root,omitempty"`
}

// ManifestElevation is why an image needs to begin as root.
type ManifestElevation struct {
	Reason string `json:"reason"`
}

// rootCapabilities are what an image that starts as root is given, and nothing
// more.
//
// Exactly the five its entrypoint needs: change ownership, bypass permission
// checks to do so, act as the owner of files it does not own, and become another
// user. Together they let it rewrite ownership anywhere in its own bind mounts
// and become any user inside itself — which is a real relaxation, and is bounded
// by the mounts being the only paths it can reach.
//
// Everything else stays dropped. NET_ADMIN, SYS_ADMIN and the rest are not on
// this list and are not reachable through it.
var rootCapabilities = []string{
	"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID",
}

type ManifestResources struct {
	MemoryLimitBytes   int64 `json:"memory_limit_bytes,omitempty"`
	MemoryMinimumBytes int64 `json:"memory_minimum_bytes,omitempty"`
	CPUShares          int   `json:"cpu_shares,omitempty"`
	DiskMinimumBytes   int64 `json:"disk_minimum_bytes,omitempty"`
}

type ManifestCredential struct {
	Ref                 string `json:"ref"`
	Description         string `json:"description"`
	Generate            *bool  `json:"generate,omitempty"`
	EnvironmentVariable string `json:"environment_variable,omitempty"`
}

type ManifestCapability struct {
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	Risk         string  `json:"risk"`
	Confirmation string  `json:"confirmation,omitempty"`
	Rollback     *string `json:"rollback,omitempty"`
}

type ManifestEvent struct {
	Type        string `json:"type"`
	Severity    string `json:"severity"`
	Description string `json:"description,omitempty"`
	Recoverable *bool  `json:"recoverable,omitempty"`
}

type ManifestRequires struct {
	MinHomebaseVersion string   `json:"min_homebase_version,omitempty"`
	Architectures      []string `json:"architectures,omitempty"`
}

// Catalogue holds the manifests this machine can install.
type Catalogue struct {
	mu        sync.RWMutex
	dir       string
	manifests map[string]Manifest
	// rejected records manifests that failed to load, so a broken catalogue
	// entry is visible in diagnostics rather than merely absent.
	rejected map[string]string
}

func NewCatalogue(dir string) *Catalogue {
	if dir == "" {
		dir = DefaultCatalogueDir
	}
	return &Catalogue{
		dir:       dir,
		manifests: make(map[string]Manifest),
		rejected:  make(map[string]string),
	}
}

// Load reads every manifest in the catalogue directory.
//
// A manifest that fails validation is skipped and recorded, not fatal. One
// malformed entry must not stop a machine's other applications from being
// manageable — but it also must not disappear silently, because "Jellyfin is
// missing from the list" is a much harder thing to diagnose than "Jellyfin's
// manifest is invalid, here is why".
func (c *Catalogue) Load() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No catalogue is a legitimate state: hostd runs perfectly well on a
			// machine with no applications installed.
			return nil
		}
		return fmt.Errorf("reading the catalogue at %s: %w", c.dir, err)
	}

	manifests := make(map[string]Manifest)
	rejected := make(map[string]string)
	// Published ports, so that two applications cannot claim the same one.
	ports := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(c.dir, entry.Name())
		manifest, err := loadManifest(path)
		if err != nil {
			rejected[entry.Name()] = err.Error()
			continue
		}

		// The id is the directory name under /srv/homebase/apps and an API path
		// segment. A manifest whose id disagrees with its filename is ambiguous
		// about which one identifies it.
		expected := strings.TrimSuffix(entry.Name(), ".json")
		if manifest.ID != expected {
			rejected[entry.Name()] = fmt.Sprintf(
				"id %q does not match the filename", manifest.ID)
			continue
		}

		if existing, clash := manifests[manifest.ID]; clash {
			rejected[entry.Name()] = fmt.Sprintf(
				"id %q is already provided by %s", manifest.ID, existing.Name)
			continue
		}

		// Two applications cannot answer on the same port.
		//
		// Checked across the catalogue rather than within one manifest, because
		// that is the only place it is visible: each file is valid on its own,
		// and the collision exists only once they are both installed. Without
		// this the second one fails at container creation with a message from
		// Docker about an address already in use, on a machine where the first
		// application is the one that stops working.
		//
		// The reserved-port list is the same idea for the ports the machine
		// itself uses; this is for the ports the catalogue hands out.
		if manifest.Network.PublishedToNetwork() {
			if taken, clash := ports[manifest.Network.PublishedPort()]; clash {
				rejected[entry.Name()] = fmt.Sprintf(
					"port %d is already published by %s",
					manifest.Network.PublishedPort(), taken)
				continue
			}
			ports[manifest.Network.PublishedPort()] = manifest.Name
		}

		manifests[manifest.ID] = manifest
	}

	c.mu.Lock()
	c.manifests = manifests
	c.rejected = rejected
	c.mu.Unlock()

	return nil
}

// loadManifest reads and validates one manifest file.
func loadManifest(path string) (Manifest, error) {
	var manifest Manifest

	// Manifests are small. A file this size being large means something is
	// wrong with it, and reading it into a root process regardless would be the
	// wrong response.
	info, err := os.Stat(path)
	if err != nil {
		return manifest, err
	}
	const maxManifestBytes = 256 * 1024
	if info.Size() > maxManifestBytes {
		return manifest, fmt.Errorf("%d bytes exceeds the %d byte limit",
			info.Size(), maxManifestBytes)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("invalid manifest: %w", err)
	}
	if dec.More() {
		return manifest, fmt.Errorf("unexpected content after the manifest")
	}

	if err := manifest.Validate(); err != nil {
		return manifest, err
	}

	return manifest, nil
}

// Validate enforces the parts of the schema that matter to a running system.
//
// The JSON Schema is checked in CI against every manifest and fixture; this is
// the same rules again at the point of use, because a manifest reaching hostd
// has come off disk on somebody's machine and CI is not there. The three
// constraints ADR-0012 and the schema call out — no privileged containers, no
// floating tags, no host paths — are re-checked here for that reason.
// reservedHostPorts are the ports on this machine an application may not take.
var reservedHostPorts = map[int]string{
	22:  "how this machine is administered over ssh",
	53:  "name resolution",
	80:  "where Homebase redirects to its own dashboard",
	443: "where the Homebase dashboard is served",
}

func (m Manifest) Validate() error {
	switch {
	case m.ManifestVersion != 1:
		return fmt.Errorf("unsupported manifest_version %d", m.ManifestVersion)
	case m.ID == "":
		return fmt.Errorf("no id")
	case m.Name == "":
		return fmt.Errorf("no name")
	case m.Container.Image == "":
		return fmt.Errorf("no container image")
	case m.Health.Type == "":
		return fmt.Errorf("no health check; an application that cannot be checked cannot be managed")
	}

	if !validAppID(m.ID) {
		return fmt.Errorf("id %q is not a valid application id", m.ID)
	}

	// A privileged container is a root shell on the host. The schema pins this
	// to false; so does this.
	if m.Permissions.Privileged {
		return fmt.Errorf("privileged containers are not permitted")
	}

	// An image that starts as root must say why, in enough words to be checked
	// against its entrypoint. A blank reason is how a relaxation becomes the
	// default: the field would spread by copying, and nobody would be able to
	// tell which applications genuinely need it.
	if elevation := m.Permissions.StartsAsRoot; elevation != nil {
		if len(strings.TrimSpace(elevation.Reason)) < 40 {
			return fmt.Errorf("starts_as_root needs a reason a reviewer can check")
		}
	}

	// "latest" moves. An application that silently changes version underneath a
	// user is an application that silently breaks, and the user has no way to
	// know why.
	if m.Container.Version == "latest" {
		return fmt.Errorf(`container.version must not be "latest"`)
	}
	if m.Container.Version == "" && m.Container.Digest == "" {
		return fmt.Errorf("container needs a version or a digest; an unpinned image is not reproducible")
	}

	switch m.Health.Type {
	case "http":
		if m.Health.Path == "" {
			return fmt.Errorf("an http health check needs a path")
		}
	case "tcp", "command", "none":
	default:
		return fmt.Errorf("unknown health check type %q", m.Health.Type)
	}

	// Publishing onto the network is the one manifest field that decides whether
	// a machine on the LAN can open this application, so what it may claim is
	// checked here as well as in the schema.
	if m.Network.PublishedToNetwork() {
		switch {
		case m.Network.InternalPort == 0:
			return fmt.Errorf(`network.reachable_from is "network" but there is no port to publish`)
		case m.Network.Protocol != "" && m.Network.Protocol != "http" && m.Network.Protocol != "https":
			return fmt.Errorf("only http and https may be published to the network, not %q",
				m.Network.Protocol)
		}
		// Ports this machine is already using. Publishing on one of them either
		// fails at container creation or, worse, takes over the address the
		// dashboard is served on — which would put an application where people
		// expect Homebase and lock everybody out of the thing that could undo
		// it.
		if reserved := reservedHostPorts[m.Network.PublishedPort()]; reserved != "" {
			return fmt.Errorf("network.host_port %d is %s; choose another",
				m.Network.PublishedPort(), reserved)
		}
	}

	// Supporting containers are checked as hard as the application's own, and
	// on one point harder: they may not be published, and there is deliberately
	// no field with which to ask.
	names := map[string]bool{}
	for _, service := range m.Services {
		switch {
		case !validServiceName.MatchString(service.Name):
			return fmt.Errorf("service name %q is not usable as a hostname", service.Name)
		case names[service.Name]:
			return fmt.Errorf("duplicate service %q", service.Name)
		case service.Name == m.ID:
			// Both would answer to that name on the private network, and which
			// one answered would be a matter of chance.
			return fmt.Errorf("service %q has the same name as the application", service.Name)
		case service.Image == "":
			return fmt.Errorf("service %q has no image", service.Name)
		case service.Version == "latest":
			return fmt.Errorf(`service %q must not use "latest"`, service.Name)
		case service.Version == "" && service.Digest == "":
			return fmt.Errorf("service %q needs a version or a digest", service.Name)
		}
		names[service.Name] = true

		for _, storage := range service.Storage {
			if storage.ID == "" || !strings.HasPrefix(storage.MountPath, "/") {
				return fmt.Errorf("service %q has an unusable storage entry", service.Name)
			}
		}
	}

	for _, other := range m.Network.Reaches {
		switch {
		case !validAppID(other):
			return fmt.Errorf("%q is not an application id", other)
		case other == m.ID:
			return fmt.Errorf("an application cannot declare it reaches itself")
		}
	}

	if len(m.Storage) == 0 {
		return fmt.Errorf("no storage declared")
	}

	seen := map[string]bool{}
	for _, storage := range m.Storage {
		switch {
		case storage.ID == "":
			return fmt.Errorf("a storage entry has no id")
		case seen[storage.ID]:
			return fmt.Errorf("duplicate storage id %q", storage.ID)
		case storage.MountPath == "" || !strings.HasPrefix(storage.MountPath, "/"):
			return fmt.Errorf("storage %q needs an absolute mount_path", storage.ID)
		}
		seen[storage.ID] = true

		switch storage.Type {
		case "private", "user-selected":
		default:
			return fmt.Errorf("storage %q has unknown type %q", storage.ID, storage.Type)
		}

		// A mount path is where the container sees the directory. `..` in it
		// would let a manifest reach outside where hostd intends to place it.
		if strings.Contains(storage.MountPath, "..") {
			return fmt.Errorf("storage %q mount_path must not contain ..", storage.ID)
		}
	}

	for _, capability := range m.Capabilities {
		switch capability.Risk {
		case "read", "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("capability %q has unknown risk %q", capability.Name, capability.Risk)
		}
	}

	for _, event := range m.Events {
		switch event.Severity {
		case "info", "warning", "error", "critical":
		default:
			return fmt.Errorf("event %q has unknown severity %q", event.Type, event.Severity)
		}
	}

	return nil
}

// validAppID mirrors the schema's pattern. The id becomes a directory name and
// a container name, so anything outside this set is refused rather than escaped.
func validAppID(id string) bool {
	if len(id) < 3 || len(id) > 40 {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		return false
	}
	last := id[len(id)-1]
	if !(last >= 'a' && last <= 'z') && !(last >= '0' && last <= '9') {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-':
		default:
			return false
		}
	}
	return true
}

// Lookup returns a manifest by id. Exact match only.
func (c *Catalogue) Lookup(id string) (Manifest, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manifest, ok := c.manifests[id]
	return manifest, ok
}

// IDs returns every installable application id, sorted.
func (c *Catalogue) IDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.manifests))
	for id := range c.manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// All returns every manifest, sorted by id.
func (c *Catalogue) All() []Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manifests := make([]Manifest, 0, len(c.manifests))
	for _, id := range c.sortedIDsLocked() {
		manifests = append(manifests, c.manifests[id])
	}
	return manifests
}

func (c *Catalogue) sortedIDsLocked() []string {
	ids := make([]string, 0, len(c.manifests))
	for id := range c.manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Rejected returns the manifests that failed to load, by filename.
//
// Surfaced rather than logged and forgotten: a missing application is far harder
// to diagnose than a rejected one with a reason attached.
func (c *Catalogue) Rejected() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.rejected))
	for name, reason := range c.rejected {
		out[name] = reason
	}
	return out
}

// Dir is where this catalogue reads from.
func (c *Catalogue) Dir() string { return c.dir }
