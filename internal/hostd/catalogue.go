package hostd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

	// Icon is one character shown on the application's tile, usually an emoji.
	//
	// Not a URL and not a file. Every alternative — an image in the package, a
	// logo fetched from the internet — is either a licensing question about
	// somebody else's trademark or a request the dashboard is forbidden from
	// making, and neither is worth it to make a grid legible. One character
	// renders in any theme at any size and costs nothing.
	//
	// Optional. Without it the tile shows the application's first letter, which
	// is worse and is not broken.
	Icon     string `json:"icon,omitempty"`
	Homepage string `json:"homepage,omitempty"`
	License  string `json:"license,omitempty"`

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

	// Provides is a service of the machine's own that this application takes
	// over — at present only "dns".
	//
	// It exists to unlock exactly one thing: a port on the reserved list. Those
	// ports are reserved because the machine itself uses them, and an
	// application that quietly took one would break the machine in a way nobody
	// would connect to having installed something. But name resolution is a
	// thing an application can legitimately *become*, and refusing to let it
	// would mean no ad blocker could ever exist here.
	//
	// So the reservation stays and the exception is declared, per application,
	// in a root-owned file reviewed in a diff — the same shape as every other
	// relaxation in this schema. It unlocks one port and no others, and only
	// one application in the catalogue may claim it.
	Provides string `json:"provides,omitempty"`

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

	// ExtraPorts are further ports this application answers on.
	//
	// Most applications have one, which is why for a long time there was only
	// one. A DNS server has two and they are not alike: the resolver on 53,
	// which every device in the house talks to, and a web interface which one
	// person opens occasionally. Serving both from one port is not something a
	// manifest can arrange.
	//
	// Held to the same rules as the main one — reserved ports, collisions
	// across the catalogue, and the requirement that publishing to the network
	// is a decision made per application rather than per port.
	ExtraPorts []ManifestPort `json:"extra_ports,omitempty"`

	Discovery []string `json:"discovery,omitempty"`
}

// ManifestPort is one more port an application answers on.
type ManifestPort struct {
	InternalPort int `json:"internal_port"`

	// HostPort defaults to InternalPort, as it does for the main one.
	HostPort int `json:"host_port,omitempty"`

	// Protocol is "tcp" or "udp"; empty means tcp. DNS needs both, which is two
	// entries rather than a field meaning "and also".
	Protocol string `json:"protocol,omitempty"`

	// Purpose is what this port is for, in words. Not used by anything that
	// runs — it is there so that a second port in a diff has to be explained.
	Purpose string `json:"purpose,omitempty"`
}

// Published is the port on the server this answers on.
func (p ManifestPort) Published() int {
	if p.HostPort > 0 {
		return p.HostPort
	}
	return p.InternalPort
}

// Transport is the protocol, defaulting to tcp.
func (p ManifestPort) Transport() string {
	if p.Protocol == "udp" {
		return "udp"
	}
	return "tcp"
}

// boundPort is one address an application answers on: a port and a protocol.
//
// Both, because they are genuinely one thing. A name server needs 53/udp and
// 53/tcp — the same number twice, two different bindings, and neither optional.
// Treating a port as just a number made the first version of this refuse the
// only manifest it was written for, which the shipped-catalogue test caught
// before anything else did.
type boundPort struct {
	Port     int
	Protocol string
}

func (b boundPort) String() string { return strconv.Itoa(b.Port) + "/" + b.Protocol }

// publishedPorts is everything an application takes on the server, main and
// extra, so that the checks which must apply to all of them are written once.
func (m Manifest) publishedPorts() []boundPort {
	if !m.Network.PublishedToNetwork() {
		return nil
	}
	// The main port is always tcp: it is http or https or it is not published,
	// which the validation above settles.
	ports := []boundPort{}
	if m.Network.PublishedPort() > 0 {
		ports = append(ports, boundPort{m.Network.PublishedPort(), "tcp"})
	}
	for _, extra := range m.Network.ExtraPorts {
		ports = append(ports, boundPort{extra.Published(), extra.Transport()})
	}
	return ports
}

// mayTakeReservedPort reports whether this application declared itself the
// thing that port is reserved for.
//
// One port per service, named here rather than left to the manifest to assert.
// "provides" is a claim about what the application is; which port that entitles
// it to is a fact about the machine.
func (m Manifest) mayTakeReservedPort(port boundPort) bool {
	// Both protocols. A resolver that answered only over UDP would work until
	// the first answer too large for a datagram, and then fail on exactly the
	// lookups that matter most.
	return m.Provides == "dns" && port.Port == 53
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

	// RunsAsRoot marks an image that is root for its whole life.
	//
	// A different thing from StartsAsRoot, and the difference is the whole
	// reason it exists rather than being folded in. StartsAsRoot is granted on
	// the promise that the entrypoint drops to PUID and PGID; an image that
	// honours neither and is granted it anyway is running as root for ever
	// under a label that says it does not. The precondition is written into the
	// schema, so the choice was to break it quietly or to name the other case.
	//
	// Neither is strictly safer than the other and this is not a weaker
	// StartsAsRoot dressed up. Root lasts longer here. What makes it
	// acceptable is the constraint that comes with it, enforced below:
	// **every storage slot must be private.** The frightening version of
	// permanent root is the one holding DAC_OVERRIDE over somebody's
	// photographs; an application that can only reach a directory Homebase made
	// for it, and that nothing else uses, cannot get there.
	RunsAsRoot *ManifestElevation `json:"runs_as_root,omitempty"`
}

// Elevated reports whether this application's container runs as uid 0.
func (p ManifestPermissions) Elevated() bool {
	return p.StartsAsRoot != nil || p.RunsAsRoot != nil
}

// ManifestElevation is why an image needs to begin as root.
type ManifestElevation struct {
	Reason string `json:"reason"`
}

// grantableCapabilities are the Linux capabilities a manifest may ask for.
//
// The schema has the same list, and this is not redundant: the schema is a
// contract checked in CI, and hostd is the thing that actually builds the
// container on somebody's machine. Until this existed a manifest could name any
// capability at all — SYS_ADMIN, or a string the kernel reads as everything —
// and hostd passed it to Docker without looking. Manifests are root-owned and
// reviewed, which is a reason to expect the list to be short, not a reason for
// the enforcer to take it on trust.
var grantableCapabilities = map[string]string{
	// The mildest of them: permission to bind a port below 1024, and nothing
	// else. It is what lets a name server have port 53 without being given a
	// root elevation, which is what the alternative would have been.
	"NET_BIND_SERVICE": "bind a port below 1024",
	"NET_ADMIN":        "configure networking",
	"NET_RAW":          "use raw sockets",
	"SYS_ADMIN":        "a very large amount; avoid",
	"SYS_NICE":         "change scheduling priority",
	"DAC_READ_SEARCH":  "bypass file read permission checks",
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
	provided := make(map[string]string)
	// Published ports, so that two applications cannot claim the same one.
	ports := make(map[boundPort]string)

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
		if clash := firstClash(manifest, ports); clash != "" {
			rejected[entry.Name()] = clash
			continue
		}
		// Only one application may be the machine's resolver. Two would both
		// try for port 53; the first to start would win and the second would
		// fail in a way that reads as the image being broken.
		if manifest.Provides != "" {
			if taken := provided[manifest.Provides]; taken != "" {
				rejected[entry.Name()] = fmt.Sprintf(
					"%s already provides %s", taken, manifest.Provides)
				continue
			}
			provided[manifest.Provides] = manifest.Name
		}
		for _, port := range manifest.publishedPorts() {
			ports[port] = manifest.Name
		}

		manifests[manifest.ID] = manifest
	}

	c.mu.Lock()
	c.manifests = manifests
	c.rejected = rejected
	c.mu.Unlock()

	return nil
}

// firstClash reports the first port this manifest wants that another already
// has, or empty if none.
//
// Recorded rather than refused at validation, because a collision is not a
// property of either manifest — both are correct and the clash exists only once
// they are in the same catalogue. Without this the second one fails at
// container creation with a message from Docker about an address already in
// use, on a machine where the *first* application is the one that stops working.
func firstClash(manifest Manifest, taken map[boundPort]string) string {
	for _, port := range manifest.publishedPorts() {
		if other, clash := taken[port]; clash {
			return fmt.Sprintf("port %s is already published by %s", port, other)
		}
	}
	return ""
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
	if elevation := m.Permissions.RunsAsRoot; elevation != nil {
		if len(strings.TrimSpace(elevation.Reason)) < 40 {
			return fmt.Errorf("runs_as_root needs a reason a reviewer can check")
		}
		// The constraint that makes permanent root tolerable, checked here
		// rather than trusted to review. An application that never drops
		// privileges must not be able to reach a folder anybody else uses:
		// root plus DAC_OVERRIDE over a shared media directory is exactly the
		// case this permission is narrow in order to exclude.
		for _, storage := range m.Storage {
			if storage.Type != "private" {
				return fmt.Errorf(
					"runs_as_root may not be combined with %s storage %q; an "+
						"application that is root for its whole life may only "+
						"reach directories of its own",
					storage.Type, storage.ID)
			}
		}
		// The two are alternatives, not a scale. Declaring both says the
		// entrypoint does and does not drop privileges.
		if m.Permissions.StartsAsRoot != nil {
			return fmt.Errorf("starts_as_root and runs_as_root are alternatives; declare one")
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
		//
		// Checked over every port, main and extra. A second port that skipped
		// this would be a way to take 443 by writing it one line further down.
		seen := map[boundPort]bool{}
		for _, port := range m.publishedPorts() {
			if port.Port < 1 || port.Port > 65535 {
				return fmt.Errorf("port %d is not a port", port.Port)
			}
			if seen[port] {
				return fmt.Errorf("port %s is published twice by this application", port)
			}
			seen[port] = true

			if reserved := reservedHostPorts[port.Port]; reserved != "" && !m.mayTakeReservedPort(port) {
				return fmt.Errorf("network.host_port %d is %s; choose another",
					port.Port, reserved)
			}
		}
	}

	// Capabilities are checked against a list here as well as in the schema.
	// hostd is what hands them to Docker, so hostd is where refusing one has to
	// happen — a manifest that reached this machine unreviewed is exactly the
	// case the schema cannot help with.
	for _, capability := range m.Permissions.Capabilities {
		if _, grantable := grantableCapabilities[capability]; !grantable {
			return fmt.Errorf("capability %q is not one an application may ask for",
				capability)
		}
	}

	// "provides" is only meaningful alongside a port it unlocks, and it unlocks
	// exactly one. A manifest claiming to be the machine's resolver while
	// publishing nothing on 53 has claimed an exception it is not using, which
	// is the state a later edit turns into one it is.
	switch m.Provides {
	case "":
	case "dns":
		if !slices.Contains(m.publishedPorts(), boundPort{53, "udp"}) {
			return fmt.Errorf(`provides is "dns" but nothing is published on port 53`)
		}
	default:
		return fmt.Errorf("unknown provides %q", m.Provides)
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
