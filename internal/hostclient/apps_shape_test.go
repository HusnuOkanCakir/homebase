package hostclient

import (
	"reflect"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/hostd"
)

// App and hostd.AppStatus describe the same thing twice, so they must agree
// about what that thing has.
//
// The duplication is deliberate — core does not import hostd's internals lightly
// and the wire format is the contract between them — but the cost is a struct
// that has to be kept in step by hand, and twice now it was not. `elevation`
// went missing first: the whole justification for granting an application root
// is that whoever installs it is shown the reason, and the field carrying that
// reason was dropped in the middle layer, so the dashboard rendered nothing and
// nothing failed. `icon` went the same way a week later.
//
// Neither had a symptom. A field that vanishes here does not error, it just
// arrives empty, and empty is indistinguishable from "this application did not
// set it".
//
// So the shape is asserted rather than reviewed. Adding a field to AppStatus now
// fails this test until it is added here too, which is the reminder that reading
// the struct carefully twice was never going to be.
func TestAppMirrorsWhatHostdReports(t *testing.T) {
	theirs := jsonFields(reflect.TypeOf(hostd.AppStatus{}))
	ours := jsonFields(reflect.TypeOf(App{}))

	for name := range theirs {
		if !ours[name] {
			t.Errorf("hostd reports %q and hostclient.App drops it; core will "+
				"serve an empty value and nothing will report a fault", name)
		}
	}
	for name := range ours {
		if !theirs[name] {
			t.Errorf("hostclient.App carries %q and hostd does not report it; "+
				"it will always be empty", name)
		}
	}
}

// jsonFields is the set of names a struct puts on the wire.
func jsonFields(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = field.Name
		}
		names[name] = true
	}
	return names
}

// SystemInfo is mirrored by hand for the same reason App is, and drifts for the
// same reason. Checked here so that the next field added to one of them cannot
// go missing in the other in silence — which is how `icon`, `elevation` and
// `path` were all lost, none of them with a symptom.
func TestSystemInfoMirrorsWhatHostdReports(t *testing.T) {
	theirs := jsonFields(reflect.TypeOf(hostd.SystemInfo{}))
	ours := jsonFields(reflect.TypeOf(SystemInfo{}))

	for name := range theirs {
		if !ours[name] {
			t.Errorf("hostd reports %q and hostclient.SystemInfo drops it; core "+
				"will serve an empty value and nothing will report a fault", name)
		}
	}
	for name := range ours {
		if !theirs[name] {
			t.Errorf("hostclient.SystemInfo carries %q and hostd does not report "+
				"it; it will always be empty", name)
		}
	}
}

// VPNStatus is mirrored by hand too, and the Tailscale block inside it is a new
// place for the same failure: a field that goes missing here arrives empty, and
// empty renders as "Tailscale is not installed" on a machine where it is the
// only thing carrying remote access.
func TestVPNStatusMirrorsWhatHostdReports(t *testing.T) {
	for _, pair := range []struct {
		name         string
		theirs, ours reflect.Type
	}{
		{"VPNStatus", reflect.TypeOf(hostd.VPNStatus{}), reflect.TypeOf(VPNStatus{})},
		{"TailscaleStatus", reflect.TypeOf(hostd.TailscaleStatus{}), reflect.TypeOf(TailscaleStatus{})},
	} {
		t.Run(pair.name, func(t *testing.T) {
			theirs := jsonFields(pair.theirs)
			ours := jsonFields(pair.ours)
			for name := range theirs {
				if !ours[name] {
					t.Errorf("hostd reports %q and hostclient drops it; it will "+
						"arrive empty and nothing will report a fault", name)
				}
			}
			for name := range ours {
				if !theirs[name] {
					t.Errorf("hostclient carries %q and hostd does not report it; "+
						"it will always be empty", name)
				}
			}
		})
	}
}
