package hostd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Sharing folders onto the local network, over SMB.
//
// The protocol Windows, macOS, Linux, phones and televisions all speak without
// installing anything — which is the whole requirement. A home server that
// cannot be a drive on somebody's laptop is a server they have to think about
// every time they use it.
//
// Homebase owns /etc/samba/smb.conf outright rather than adding to it. Partial
// ownership means two sources of truth for what is shared, and the failure it
// produces is a folder that is still on the network after Homebase was told to
// stop sharing it. An existing configuration is copied aside once, never
// merged — see writeSambaConfig.
//
// The record of what is shared is this package's own state file, and smb.conf is
// rendered from it. Reading configuration back out of smb.conf would mean
// parsing a format with includes, macro expansion and per-share defaults, and
// getting that wrong means showing somebody a list of shares that is not the
// list of shares.

const (
	sambaConfig      = "/etc/samba/smb.conf"
	sambaConfigSaved = "/etc/samba/smb.conf.before-homebase"

	// sharesDirName is where share folders live inside a storage location, so
	// that they are visibly separate from application data on the same disk.
	sharesDirName = "shares"

	// shareNamePattern is what a share may be called. It becomes a directory
	// name and an SMB share name, which Windows shows as a folder.
	shareNamePattern = `^[a-z][a-z0-9-]{0,30}[a-z0-9]$`

	// shareUserPrefix namespaces the accounts Homebase creates for file
	// sharing, so that none of them can collide with a login that administers
	// the machine. A file-sharing password is typed into a Windows dialog and
	// saved there for ever; it must not also be a way to log in.
	shareUserPrefix = "hbshare-"
)

var validShareName = regexp.MustCompile(shareNamePattern)

// Share is one folder published onto the local network.
type Share struct {
	Name string `json:"name"`

	// Location is the storage location holding it.
	Location string `json:"location"`

	// ReadOnly publishes it without allowing writes.
	ReadOnly bool `json:"read_only"`

	AddedAt string `json:"added_at"`
}

// ShareState is a share plus what is currently true about it.
type ShareState struct {
	Share

	// Path is where the folder is on the server.
	Path string `json:"path"`

	// Available is whether the disk holding it is there. A share whose disk is
	// unplugged still exists and is still configured; it simply has nothing
	// behind it, and that is a different thing from not being shared.
	Available bool `json:"available"`

	// Address is what to type on another machine.
	Address string `json:"address"`
}

// ShareStatus is everything about file sharing on this server.
type ShareStatus struct {
	// Installed is whether the SMB server is on this machine at all. Sharing is
	// not part of the base installation: it is a listening service on the local
	// network, and one that is off until asked for is one that cannot be
	// misconfigured.
	Installed bool `json:"installed"`

	// Running is whether it is actually serving. Separate from Installed and
	// from there being shares, because "configured but not running" is a real
	// state and the one worth reporting loudly.
	Running bool `json:"running"`

	Shares []ShareState `json:"shares"`

	// Users are the accounts that may connect. Names only — Homebase has no way
	// to read a password back and would not report one if it had.
	Users []string `json:"users"`

	// ServerName is what the server is called on the network.
	ServerName string `json:"server_name"`
}

// ShareServices is what the sharing operations need.
type ShareServices struct {
	storage   *StorageServices
	stateFile string
}

func NewShareServices(storage *StorageServices, stateDir string) *ShareServices {
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return &ShareServices{
		storage:   storage,
		stateFile: filepath.Join(stateDir, "shares.json"),
	}
}

// --- State --------------------------------------------------------------------

func (s *ShareServices) load() ([]Share, error) {
	data, err := os.ReadFile(s.stateFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading " + s.stateFile + ": " + err.Error())
	}
	var shares []Share
	if err := json.Unmarshal(data, &shares); err != nil {
		// Refused rather than reset, for the same reason as storage: this file
		// says what is on the network, and starting again from empty would
		// unshare somebody's folders while reporting nothing wrong.
		return nil, internalError("the share configuration in " + s.stateFile +
			" could not be read: " + err.Error())
	}
	sort.Slice(shares, func(i, j int) bool { return shares[i].Name < shares[j].Name })
	return shares, nil
}

func (s *ShareServices) save(shares []Share) error {
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		return internalError("creating the state directory: " + err.Error())
	}
	body, err := json.MarshalIndent(shares, "", "  ")
	if err != nil {
		return internalError("encoding the share configuration: " + err.Error())
	}
	temporary := s.stateFile + ".new"
	if err := os.WriteFile(temporary, append(body, '\n'), 0o600); err != nil {
		return internalError("writing " + temporary + ": " + err.Error())
	}
	if err := os.Rename(temporary, s.stateFile); err != nil {
		os.Remove(temporary)
		return internalError("saving the share configuration: " + err.Error())
	}
	return nil
}

// --- Reading the state of things -----------------------------------------------

func (s *ShareServices) status(ctx context.Context) (ShareStatus, error) {
	status := ShareStatus{
		Installed:  sambaInstalled(),
		Running:    unitIsActive(ctx, "smbd.service"),
		ServerName: serverName(),
		Users:      s.shareUsers(),
	}

	shares, err := s.load()
	if err != nil {
		return status, err
	}
	for _, share := range shares {
		status.Shares = append(status.Shares, s.describe(share, status.ServerName))
	}
	return status, nil
}

func (s *ShareServices) describe(share Share, server string) ShareState {
	state := ShareState{
		Share:   share,
		Address: `\\` + server + `\` + share.Name,
	}
	if mountPoint, ok := s.storage.ResolveLocation(share.Location); ok {
		state.Path = filepath.Join(mountPoint, sharesDirName, share.Name)
		state.Available = true
	}
	return state
}

// serverName is what this machine answers to on the network.
func serverName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "homebase"
	}
	return name
}

func sambaInstalled() bool {
	_, err := exec.LookPath("smbd")
	return err == nil
}

// shareUsers lists the accounts Homebase created for file sharing.
//
// Read from the system rather than recorded separately: the accounts are the
// truth, and a list kept beside them is a list that can be wrong.
func (s *ShareServices) shareUsers() []string {
	content, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil
	}
	var users []string
	for _, line := range strings.Split(string(content), "\n") {
		name, _, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(name, shareUserPrefix) {
			users = append(users, strings.TrimPrefix(name, shareUserPrefix))
		}
	}
	sort.Strings(users)
	return users
}

// --- Writing the configuration --------------------------------------------------

// renderSambaConfig produces the whole of smb.conf.
//
// Written by hand rather than by editing what is there, and deliberately short.
// Every line is either required or is a decision worth being able to see in one
// screen — which is the point of owning the file rather than adding to it.
func renderSambaConfig(server string, shares []ShareState) string {
	var out strings.Builder

	out.WriteString("# Written by Homebase. Changes here are replaced.\n")
	out.WriteString("# The shares Homebase knows about are in " +
		filepath.Join(DefaultStateDir, "shares.json") + ".\n\n")

	out.WriteString("[global]\n")
	out.WriteString("   workgroup = WORKGROUP\n")
	out.WriteString("   server string = " + server + "\n")
	out.WriteString("   netbios name = " + server + "\n")
	// Passwords, always. `security = user` is Samba's default and is restated
	// because the alternative is a share anybody on the network can open.
	out.WriteString("   security = user\n")
	out.WriteString("   map to guest = never\n")
	// SMB1 is the protocol with the wormable bugs, off by default since Windows
	// 10 and disabled here so that nothing turns it back on to make an old
	// device work. A device that needs SMB1 needs replacing.
	out.WriteString("   server min protocol = SMB2_10\n")
	out.WriteString("   client min protocol = SMB2_10\n")
	// Signing available but not required: required signing costs about a third
	// of the throughput on the kind of CPU that is in a laptop from 2013, and
	// this is a local network where the alternative to Homebase is a USB stick
	// carried between rooms.
	out.WriteString("   server signing = auto\n")
	// Only the local network. The way into this server from outside is the VPN,
	// where the whole tunnel is encrypted — SMB exposed to the internet is one
	// of the most attacked services there is.
	out.WriteString("   bind interfaces only = yes\n")
	out.WriteString("   interfaces = lo " + strings.Join(localInterfaces(), " ") + "\n")
	out.WriteString("   hosts allow = 127.0.0.1 " + strings.Join(privateNetworks, " ") + "\n")
	out.WriteString("   hosts deny = 0.0.0.0/0\n")
	out.WriteString("   log level = 1\n")
	out.WriteString("   disable netbios = yes\n")
	out.WriteString("   smb ports = 445\n")

	for _, share := range shares {
		out.WriteString("\n[" + share.Name + "]\n")
		out.WriteString("   path = " + share.Path + "\n")
		out.WriteString("   browseable = yes\n")
		if share.ReadOnly {
			out.WriteString("   read only = yes\n")
		} else {
			out.WriteString("   read only = no\n")
		}
		out.WriteString("   valid users = @" + serviceAccount + "\n")
		// Everything lands owned by the service account's group, so that files
		// written from a Windows laptop are readable by an application on the
		// server and by the backup, rather than by whichever account happened
		// to write them first.
		out.WriteString("   force group = " + serviceAccount + "\n")
		out.WriteString("   create mask = 0664\n")
		out.WriteString("   directory mask = 0775\n")
		out.WriteString("   veto files = /.DS_Store/Thumbs.db/desktop.ini/\n")
		out.WriteString("   delete veto files = yes\n")
	}
	return out.String()
}

// privateNetworks are the address ranges a home network uses. Sharing is offered
// to those and to nothing else.
var privateNetworks = []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12"}

// localInterfaces names the cards Samba should listen on: the real ones, never
// the Docker bridges. A container that can reach the file server is a container
// that can read everything on it.
func localInterfaces() []string {
	var names []string
	for _, iface := range ReadNetworkStatus().Interfaces {
		if iface.Kind == "ethernet" || iface.Kind == "wireless" {
			names = append(names, iface.Name)
		}
	}
	return names
}

// writeSambaConfig replaces smb.conf, keeping a copy of anything that was there
// before Homebase first touched it.
func writeSambaConfig(content string) error {
	existing, err := os.ReadFile(sambaConfig)
	if err == nil && !strings.HasPrefix(string(existing), "# Written by Homebase") {
		if _, err := os.Stat(sambaConfigSaved); os.IsNotExist(err) {
			// Once, and only once. Copied rather than moved so that a failure
			// after this point leaves a working server rather than none.
			if err := os.WriteFile(sambaConfigSaved, existing, 0o644); err != nil {
				return internalError("keeping a copy of the existing " +
					sambaConfig + ": " + err.Error())
			}
		}
	}
	return writeRootFile(sambaConfig, content, 0o644)
}

// setShareFirewall opens or closes the file-sharing port.
//
// Failures are logged into the error path of nothing: a share that works while
// the firewall did not change is a share nobody can reach, and that is visible
// immediately and fixable by hand. Refusing to share at all because ufw was
// removed from the machine would be worse.
func setShareFirewall(ctx context.Context, action string) {
	request := "# Written by Homebase for homebase-share-firewall.service.\n" +
		"action=" + action + "\n"
	if err := writeRootFile(shareFirewallRequest, request, 0o600); err != nil {
		return
	}
	defer os.Remove(shareFirewallRequest)
	_, _ = runUpdateUnit(ctx, "homebase-share-firewall.service")
}

const shareFirewallRequest = "/etc/homebase/share-firewall"

// enableUnit makes a unit start at boot, and starts it now.
//
// The symlink is written here rather than by `systemctl enable`, and the reason
// is narrow: smbd ships a SysV init script, so `systemctl enable` also runs
// systemd-sysv-install, which runs update-rc.d, which writes to /etc/rc*.d —
// read-only under this service's ProtectSystem=strict. The helper fails, and
// systemctl gives up before writing the symlink at all, so the unit ends up
// neither enabled nor started while reporting an error about a directory that
// has nothing to do with file sharing.
//
// Mount units go through `systemctl enable` and always have. They have no init
// script behind them, so nothing invokes the compatibility path.
//
// /etc/systemd/system is already writable by this service, for those mount
// units, so this needs no new grant — and the link it writes is exactly the one
// `systemctl enable` would have written.
func enableUnit(ctx context.Context, unit string) error {
	target, err := unitFilePath(ctx, unit)
	if err != nil {
		return err
	}
	wants := "/etc/systemd/system/multi-user.target.wants"
	if err := os.MkdirAll(wants, 0o755); err != nil {
		return internalError("creating " + wants + ": " + err.Error())
	}
	link := filepath.Join(wants, unit)
	if existing, err := os.Readlink(link); err == nil && existing == target {
		return nil
	}
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		return internalError("enabling " + unit + ": " + err.Error())
	}
	return runSystemctl(ctx, "daemon-reload")
}

// disableUnit is the same in reverse, for the same reason.
func disableUnit(ctx context.Context, unit string) {
	_ = os.Remove(filepath.Join("/etc/systemd/system/multi-user.target.wants", unit))
	_ = runSystemctl(ctx, "stop", unit)
	_ = runSystemctl(ctx, "daemon-reload")
}

// unitFilePath asks systemd where a unit's file is, rather than guessing between
// /lib and /usr/lib, which differ by distribution and by version.
func unitFilePath(ctx context.Context, unit string) (string, error) {
	out, err := systemctlShow(ctx, unit, "FragmentPath")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", &Error{
			Code:        "share.no_file_server",
			Message:     "The file server is not on this machine.",
			Detail:      unit + " has no unit file",
			Recoverable: true,
			Recovery:    "Share a folder to have Homebase install it.",
			Status:      500,
		}
	}
	return strings.TrimSpace(out), nil
}

// apply writes the configuration and makes the server match it.
func (s *ShareServices) apply(ctx context.Context, shares []Share) error {
	server := serverName()
	states := make([]ShareState, 0, len(shares))
	for _, share := range shares {
		state := s.describe(share, server)
		if !state.Available {
			// A share whose disk is not there is left out of the configuration
			// rather than pointed at a path that is not a disk. Samba would
			// otherwise create the directory on the system disk and serve an
			// empty folder that looks exactly like the one somebody's files
			// were in — the same failure applications are protected from.
			continue
		}
		states = append(states, state)
	}

	if err := writeSambaConfig(renderSambaConfig(server, states)); err != nil {
		return err
	}

	// nmbd is NetBIOS name service, which is SMB1-era and is used by nothing
	// current. Windows finds this server by its .local name.
	disableUnit(ctx, "nmbd.service")

	if len(states) == 0 {
		// Nothing to serve. Stopped rather than left running with an empty
		// configuration, and the port closed behind it: a listening service
		// with no purpose is surface, and an open port with nothing behind it
		// is surface somebody has forgotten about.
		disableUnit(ctx, "smbd.service")
		setShareFirewall(ctx, "close")
		return nil
	}

	if err := enableUnit(ctx, "smbd.service"); err != nil {
		return err
	}
	// After the server is configured and before it is started, so there is never
	// a moment when the port is open onto a stale configuration.
	setShareFirewall(ctx, "open")

	if err := runSystemctl(ctx, "restart", "smbd.service"); err != nil {
		return &Error{
			Code:        "share.could_not_start",
			Message:     "Homebase could not start the file server.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Run `journalctl -u smbd` to see what it said.",
			Status:      500,
		}
	}
	return nil
}

// --- Accounts -------------------------------------------------------------------

// shareAccountRequest is the root-only file the account helper reads.
const shareAccountRequest = "/etc/homebase/share-account"

// makeShareAccount creates or removes the Unix account behind a file-sharing
// login, through the one unit that may write the credential store.
//
// The name is validated here and again in the script. Twice on purpose: this
// side is where a caller's string arrives, and that side is where the rule about
// what may be created is enforced without trusting who wrote the file.
func makeShareAccount(ctx context.Context, action, username string) error {
	if !validShareName.MatchString(username) {
		return internalError("refusing the account name " + username)
	}
	request := "# Written by Homebase for homebase-share-account.service.\n" +
		"action=" + action + "\n" +
		"account=" + shareUserPrefix + username + "\n"
	if err := writeRootFile(shareAccountRequest, request, 0o600); err != nil {
		return err
	}
	// Removed either way. It is a request, not configuration, and one left
	// behind would be acted on again by a stray start of the unit.
	defer os.Remove(shareAccountRequest)

	if out, err := runUpdateUnit(ctx, "homebase-share-account.service"); err != nil {
		return internalError("the account could not be " + action + "ed: " +
			strings.TrimSpace(out) + ": " + err.Error())
	}
	return nil
}

// setSharePassword creates the account if it is new and sets its file-sharing
// password.
//
// The Unix account has no shell and no password of its own, and its name is
// prefixed, so it is a way to open a folder and not a way to log in.
//
// The password goes to smbpasswd on standard input and nowhere else. Not an
// argument, because arguments are in /proc for every process on the machine for
// as long as the command runs; and not through the account helper, because that
// would mean writing it to a file — which is why the split between the two
// exists at all.
func setSharePassword(ctx context.Context, username, password string) error {
	if err := makeShareAccount(ctx, "add", username); err != nil {
		return err
	}

	smbpasswd, err := exec.LookPath("smbpasswd")
	if err != nil {
		return internalError("smbpasswd is not on this machine")
	}
	account := shareUserPrefix + username

	set := exec.CommandContext(ctx, smbpasswd, "-s", "-a", account)
	set.Env = aptEnv()
	set.Stdin = strings.NewReader(password + "\n" + password + "\n")
	if out, err := set.CombinedOutput(); err != nil {
		return internalError("setting the password: " + strings.TrimSpace(string(out)))
	}

	enable := exec.CommandContext(ctx, smbpasswd, "-e", account)
	enable.Env = aptEnv()
	if out, err := enable.CombinedOutput(); err != nil {
		return internalError("enabling the account: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func removeShareUser(ctx context.Context, username string) error {
	account := shareUserPrefix + username
	if _, err := user.Lookup(account); err != nil {
		return &Error{
			Code:        "share.no_such_user",
			Message:     "There is no file-sharing account by that name.",
			Detail:      account,
			Recoverable: true,
			Recovery:    "Run `homebasectl share` to see the accounts.",
			Status:      404,
		}
	}
	// Samba's database first, which hostd may write. If the account helper then
	// fails, what is left is a Unix account that cannot open anything — the
	// harmless half of the pair.
	if smbpasswd, err := exec.LookPath("smbpasswd"); err == nil {
		remove := exec.CommandContext(ctx, smbpasswd, "-x", account)
		remove.Env = aptEnv()
		_ = remove.Run()
	}
	return makeShareAccount(ctx, "remove", username)
}

// --- The share folder --------------------------------------------------------------

// makeShareFolder creates the directory and gives it to the service account.
//
// Group-writable, so that a file copied from a Windows laptop can be read by the
// backup that will copy it away and by an application on the same disk.
//
// Not set-group-id, which would be the usual way to guarantee that: hostd's unit
// sets RestrictSUIDSGID=yes, so the kernel refuses it — `operation not
// permitted` on chmod, as root, which is a confusing enough error to be worth
// naming here. The restriction is right and stays: a root service that cannot
// create a setuid file is one that cannot leave a way back in.
//
// Samba does the job instead, with `force group` in the configuration. That is
// better placed anyway, because it applies to the files rather than depending on
// a bit inherited from a directory.
func (s *ShareServices) makeShareFolder(share Share) (string, error) {
	mountPoint, ok := s.storage.ResolveLocation(share.Location)
	if !ok {
		return "", &Error{
			Code:        "share.disk_not_available",
			Message:     "That disk is not connected, so nothing can be shared from it.",
			Detail:      share.Location,
			Recoverable: true,
			Recovery:    "Plug the disk in and try again.",
			Status:      409,
		}
	}

	path := filepath.Join(mountPoint, sharesDirName, share.Name)
	if err := os.MkdirAll(path, 0o775); err != nil {
		return "", internalError("creating " + path + ": " + err.Error())
	}
	if err := giveToServiceGroup(path); err != nil {
		return "", err
	}
	// MkdirAll applies the umask, so the mode asked for is not the mode created.
	// Set again explicitly.
	if err := os.Chmod(path, 0o775); err != nil {
		return "", internalError("setting permissions on " + path + ": " + err.Error())
	}
	return path, nil
}

// installSamba fetches the SMB server, which is not part of the base install.
//
// Through the same unit the updates go through: hostd cannot open a network
// socket, so anything that reaches the network is a fixed command in a
// package-installed file rather than something composed here.
func installSamba(ctx context.Context) error {
	if sambaInstalled() {
		return nil
	}

	// Waited for, unlike the update units. Those cannot be synchronous because
	// applying an update restarts hostd, so the process holding the request open
	// is the process being replaced. Installing a package does not touch hostd,
	// so there is no reason to hand the caller a job to poll and every reason
	// not to: this is one command whose answer is "it is installed" or "it is
	// not", and the caller is a person waiting at a prompt.
	//
	// The first version of this started the unit detached and polled, which was
	// also a race — `systemctl is-active` reports a running oneshot unit as
	// "activating", so the poll saw "not active, not installed" on its first
	// pass and reported failure four tenths of a second after starting.
	out, err := runUpdateUnit(ctx, "homebase-install-samba.service")
	if err != nil {
		return &Error{
			Code:        "share.could_not_install",
			Message:     "Homebase could not install the file server.",
			Detail:      strings.TrimSpace(out) + ": " + err.Error(),
			Recoverable: true,
			Recovery: "Check that this server can reach the internet with " +
				"`homebasectl network`, then try again.",
			Status: 503,
		}
	}
	if !sambaInstalled() {
		return &Error{
			Code:        "share.could_not_install",
			Message:     "Homebase could not install the file server.",
			Detail:      "the installation finished without smbd being present",
			Recoverable: true,
			Recovery: "Run `journalctl -u homebase-install-samba` to see what apt " +
				"said.",
			Status: 500,
		}
	}
	return nil
}

func unknownShare(name string) *Error {
	return &Error{
		Code:        "share.not_found",
		Message:     "There is no shared folder by that name.",
		Detail:      name,
		Recoverable: true,
		Recovery:    "Run `homebasectl share` to see what is shared.",
		Status:      404,
	}
}

func findShare(shares []Share, name string) (Share, int, bool) {
	for i, share := range shares {
		if share.Name == name {
			return share, i, true
		}
	}
	return Share{}, 0, false
}
