package hostd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Applications made of more than one container.
//
// Immich needs a database and a cache. Paperless needs a database, a cache, and
// two converters. Nextcloud needs a database. None of them is unusual, and none
// of them fits an application that is one container — so the manifest describes
// the supporting ones and everything here arranges them.
//
// Three rules, and they are the whole design:
//
//   **Nothing supporting is ever published.** Each application gets a private
//   network and its services join only that. There is no manifest field with
//   which to ask for a port on one, which is deliberate: a database a manifest
//   *could* publish is a database somebody eventually publishes.
//
//   **The application is the last thing started and the first thing stopped.**
//   A database that comes up after the thing using it produces an application
//   that crash-loops for reasons visible only in a log nobody is reading.
//
//   **Supporting storage is private, always.** A database on a disk somebody
//   can unplug is a database that vanishes, and unlike a media folder there is
//   nothing sensible for the application to do about its absence.
//
// The network is the mechanism behind the first rule. Without it a database
// would sit on Docker's default bridge, reachable by every other container on
// the machine — which is every other application. A network of its own is what
// makes "this database belongs to this application" something the machine
// enforces rather than something the manifest intends.

// networkName is an application's private network.
func networkName(id string) string { return containerPrefix + id }

// serviceContainerName is one supporting container.
//
// Prefixed with the application's own name so that everything belonging to one
// application sorts together in `docker ps` — which is where somebody looks
// when they are trying to work out what is running and why.
func serviceContainerName(app, service string) string {
	return containerPrefix + app + "-" + service
}

// serviceDataDir is where a supporting container's private storage lives.
//
// Under the application's own directory, so that removing an application's data
// removes its database with it. A database left behind after its application is
// gone is a directory nobody will ever identify.
func (s *AppServices) serviceDataDir(app, service, id string) string {
	return filepath.Join(s.appDataDir(app), "services", service, id)
}

// joinedNetworks are the networks an application's own container attaches to.
//
// Its own, always — that is where its supporting containers live and where other
// applications find it. Plus one for each application it declared it reaches, so
// the name resolves.
//
// Attached at creation rather than connected afterwards, for the same reason the
// supporting containers are: a container that starts before it is on the network
// fails its first connection, and an application whose download client is
// briefly unreachable at startup is one that logs a failure nobody reads.
func (s *AppServices) joinedNetworks(manifest Manifest) map[string]endpointConfig {
	networks := map[string]endpointConfig{
		networkName(manifest.ID): {Aliases: []string{manifest.ID}},
	}
	for _, other := range manifest.Network.Reaches {
		if other == manifest.ID {
			continue
		}
		// The alias is this application's own id on the other's network, so that
		// the traffic is identifiable from either end.
		networks[networkName(other)] = endpointConfig{Aliases: []string{manifest.ID}}
	}
	return networks
}

// ensureNetworks creates every network an application needs to be on.
//
// Including the ones belonging to applications it reaches, which may not be
// installed yet — creating an empty network costs nothing and means the order
// applications are installed in does not matter. Sonarr set up before qBittorrent
// otherwise fails at creation with a message about a missing network, which is
// true and useless.
func (s *AppServices) ensureNetworks(ctx context.Context, manifest Manifest) error {
	for name := range s.joinedNetworks(manifest) {
		if err := s.docker.createNetwork(ctx, name); err != nil {
			return wrapDockerError(err, "network_failed",
				"Homebase could not set up the network "+manifest.Name+" needs.",
				"Try again. If it keeps failing, check that Docker is healthy.")
		}
	}
	return nil
}

// connectExtraNetworks attaches an application to the networks of the
// applications it reaches, after its container exists and before it starts.
//
// Failures are returned rather than logged. An application that cannot reach its
// download client is not working, and reporting it as installed would leave
// somebody configuring a connection that cannot succeed.
func (s *AppServices) connectExtraNetworks(ctx context.Context, manifest Manifest) error {
	own := networkName(manifest.ID)
	for name, endpoint := range s.joinedNetworks(manifest) {
		if name == own {
			continue
		}
		if err := s.docker.connectNetwork(ctx, name,
			containerName(manifest.ID), endpoint.Aliases); err != nil {
			return wrapDockerError(err, "network_failed",
				"Homebase could not connect "+manifest.Name+" to the applications it needs.",
				"Try installing it again.")
		}
	}
	return nil
}

// startServices brings up everything an application needs before it starts.
//
// Idempotent: a container that is already there is left alone and started, so
// this runs on every start rather than only on the first.
func (s *AppServices) startServices(ctx context.Context, manifest Manifest, as owner) error {
	// The networks first, and always — an application with no supporting
	// containers still needs its own, because that is where other applications
	// find it.
	if err := s.ensureNetworks(ctx, manifest); err != nil {
		return err
	}
	if len(manifest.Services) == 0 {
		return nil
	}

	for _, service := range manifest.Services {
		if err := s.startOneService(ctx, manifest, service, as); err != nil {
			return err
		}
	}
	return nil
}

func (s *AppServices) startOneService(ctx context.Context, manifest Manifest,
	service ManifestService, as owner) error {
	name := serviceContainerName(manifest.ID, service.Name)

	state, err := s.docker.inspectContainer(ctx, name)
	if err != nil {
		return err
	}
	if state != nil {
		if state.State.Running {
			return nil
		}
		return s.docker.startContainer(ctx, name)
	}

	reference := service.Reference()
	if err := s.docker.pullImage(ctx, reference, nil); err != nil {
		// The same reasoning as the application's own image: it is pinned, so a
		// copy already on disk is the same bytes, and a server whose broadband
		// is down should still be able to start what it already has.
		if !s.docker.hasImage(ctx, reference) {
			return wrapDockerError(err, "pull_failed",
				"Homebase could not download the "+service.Name+" that "+
					manifest.Name+" needs.",
				"Check that the server is connected to the internet, then try again.")
		}
	}

	binds, err := s.prepareServiceStorage(manifest, service, as)
	if err != nil {
		return err
	}

	if _, err := s.docker.createContainer(ctx, name,
		s.buildServiceContainer(manifest, service, binds, as)); err != nil {
		return wrapDockerError(err, "create_failed",
			"Homebase could not set up the "+service.Name+" that "+manifest.Name+" needs.",
			"Try installing it again.")
	}
	if err := s.docker.startContainer(ctx, name); err != nil {
		return wrapDockerError(err, "start_failed",
			"The "+service.Name+" that "+manifest.Name+" needs would not start.",
			"Check the application's logs for the reason.")
	}
	return nil
}

// buildServiceContainer is the application's own container specification with
// everything removed that a supporting container has no business having.
//
// Written as its own function rather than by adjusting the application's, so
// that what a service may do is readable in one place. Every difference here is
// a restriction.
func (s *AppServices) buildServiceContainer(manifest Manifest, service ManifestService,
	binds []string, as owner) containerConfig {

	config := containerConfig{
		Image: service.Reference(),
		// The same account as the application. They share a private directory
		// tree, and two accounts would mean each could not read the other's.
		User: as.String(),
		Labels: map[string]string{
			"homebase.app":     manifest.ID,
			"homebase.service": service.Name,
			"homebase.managed": "true",
		},
		HostConfig: hostConfig{
			Binds:         binds,
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			// The application's private network and nothing else. There are
			// deliberately no PortBindings and no ExposedPorts here, and no
			// manifest field that could produce any.
			NetworkMode: networkName(manifest.ID),
			Privileged:  false,
		},
		NetworkingConfig: &networkingConfig{
			EndpointsConfig: map[string]endpointConfig{
				// The name the application connects to. Attached at creation
				// rather than afterwards: a container that starts before it is
				// on the network fails its first connection.
				networkName(manifest.ID): {Aliases: []string{service.Name}},
			},
		},
	}

	for key, value := range service.Environment {
		config.Env = append(config.Env, key+"="+value)
	}

	// A share of the application's own limit rather than a second helping of
	// it. Without this an application declaring four services could use five
	// times what its manifest says.
	if manifest.Resources.MemoryLimitBytes > 0 {
		config.HostConfig.Memory = manifest.Resources.MemoryLimitBytes / 2
	}
	return config
}

// prepareServiceStorage creates a supporting container's private directories.
func (s *AppServices) prepareServiceStorage(manifest Manifest, service ManifestService,
	as owner) ([]string, error) {

	var binds []string
	for _, storage := range service.Storage {
		path := s.serviceDataDir(manifest.ID, service.Name, storage.ID)
		if err := os.MkdirAll(path, 0o750); err != nil {
			return nil, internalError("creating " + path + ": " + err.Error())
		}
		if os.Geteuid() == 0 {
			if err := giveTo(path, as); err != nil {
				return nil, internalError("setting ownership on " + path + ": " + err.Error())
			}
		}
		binds = append(binds, path+":"+storage.MountPath+":rw")
	}
	return binds, nil
}

// stopServices takes the supporting containers down, after the application.
func (s *AppServices) stopServices(ctx context.Context, manifest Manifest) {
	for _, service := range manifest.Services {
		_ = s.docker.stopContainer(ctx,
			serviceContainerName(manifest.ID, service.Name), stopGraceSeconds)
	}
}

// removeServices deletes the supporting containers and the network.
//
// The network last, because a network with a container still attached cannot be
// removed — and the failure is reported as though the network were in use by
// something else.
func (s *AppServices) removeServices(ctx context.Context, manifest Manifest) []string {
	var problems []string
	for _, service := range manifest.Services {
		name := serviceContainerName(manifest.ID, service.Name)
		_ = s.docker.stopContainer(ctx, name, stopGraceSeconds)
		if err := s.docker.removeContainer(ctx, name, true); err != nil {
			problems = append(problems,
				fmt.Sprintf("%s could not be removed: %v", service.Name, err))
		}
	}
	if len(manifest.Services) > 0 {
		if err := s.docker.removeNetwork(ctx, networkName(manifest.ID)); err != nil {
			problems = append(problems,
				fmt.Sprintf("the private network could not be removed: %v", err))
		}
	}
	return problems
}

// serviceStates reports whether each supporting container is running.
//
// Used to explain an application that will not start. "Jellyfin is not running"
// and "Jellyfin's database is not running" need different answers, and without
// this they produce the same one.
func (s *AppServices) serviceStates(ctx context.Context, manifest Manifest) []ServiceState {
	if len(manifest.Services) == 0 {
		return nil
	}
	states := make([]ServiceState, 0, len(manifest.Services))
	for _, service := range manifest.Services {
		state := ServiceState{Name: service.Name}
		if inspected, err := s.docker.inspectContainer(ctx,
			serviceContainerName(manifest.ID, service.Name)); err == nil && inspected != nil {
			state.Installed = true
			state.Running = inspected.State.Running && !inspected.State.Restarting
		}
		states = append(states, state)
	}
	return states
}

// ServiceState is what one supporting container is doing.
type ServiceState struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
}

// unhealthyServices names the ones that are not running, for a message.
func unhealthyServices(states []ServiceState) string {
	var stopped []string
	for _, state := range states {
		if !state.Running {
			stopped = append(stopped, state.Name)
		}
	}
	return strings.Join(stopped, ", ")
}
