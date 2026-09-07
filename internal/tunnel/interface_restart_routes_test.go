package tunnel

import (
	"errors"
	"reflect"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/routing"
)

func TestRestartRestoresDependentsOnlyAfterSuccessfulStart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var calls []string
		failure := errors.New("start failed")
		err := restartWithNewSettingsSteps(
			func() error { calls = append(calls, "stop"); return nil },
			func() error { calls = append(calls, "save"); return nil },
			func() error { calls = append(calls, "regenerate"); return nil },
			func() error {
				return startAndReapplySteps(func() error {
					calls = append(calls, "start")
					if fail {
						return failure
					}
					return nil
				}, func() { calls = append(calls, "reapply") })
			},
		)
		want := []string{"stop", "save", "regenerate", "start"}
		if fail {
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
		} else {
			want = append(want, "reapply")
			if err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

func TestReapplyDependentsWithRoutingInitialized(t *testing.T) {
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	old := routing.TryGet()
	t.Cleanup(func() { routing.SetInstance(old) })
	iface := newTestIface()
	routing.SetInstance(nil)
	iface.reapplyDependents()
	routing.SetInstance(routing.New())
	iface.reapplyDependents()
}
