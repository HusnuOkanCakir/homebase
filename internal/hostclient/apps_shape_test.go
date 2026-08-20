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
