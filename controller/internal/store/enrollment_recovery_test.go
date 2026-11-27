package store

import (
	"context"
	"testing"
)

// A fully re-initialised AP (reflash, board swap) loses /etc/ac/enrollment.key
// and state.json: it comes back with a NEW device key and a reset generation
// that restarted at zero. These tests pin down how the controller treats such
// an enrollment and which recovery paths work.
func TestRegisterEnrollmentReflashedAP(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const mac = "aa:bb:cc:dd:ee:ff"
	d := Device{MAC: mac, Hostname: "ap1"}

	// First enrollment (old identity), then adopted.
	if ok, err := st.RegisterEnrollment(ctx, d, "pub-old", "id-old", 7); err != nil || !ok {
		t.Fatalf("first enroll: ok=%v err=%v", ok, err)
	}
	if err := st.MarkAdopted(ctx, mac, "tpl", 3); err != nil {
		t.Fatal(err)
	}

	// The re-initialised AP: new key, generation restarted at 0. It must not
	// silently replace the stored binding — that rejection is what the agent
	// surfaces as "already registered to another AP identity".
	if ok, err := st.RegisterEnrollment(ctx, d, "pub-new", "id-new", 0); err != nil || ok {
		t.Fatalf("reflash enroll: ok=%v err=%v, want rejected", ok, err)
	}

	// An operator release authorizes a replacement without deleting history.
	if err := st.Release(ctx, mac); err != nil {
		t.Fatal(err)
	}
	previous, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAction(ctx, mac, "reboot", nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RegisterEnrollment(ctx, d, "pub-new", "id-new", 0); err != nil || !ok {
		t.Fatalf("enroll after release: ok=%v err=%v", ok, err)
	}
	if public, id, err := st.EnrollmentCredentials(ctx, mac); err != nil || public != "pub-new" || id != "id-new" {
		t.Fatalf("replacement credentials = %q/%q, %v", public, id, err)
	}
	if got, err := st.GetDevice(ctx, mac); err != nil || got.State != StatePending || got.ConfigRev != 3 {
		t.Fatalf("replacement must stay pending and retain revision: %+v, %v", got, err)
	}
	if actions, err := st.PendingActions(ctx, mac, 10); err != nil || len(actions) != 0 {
		t.Fatalf("old commands survived identity replacement: %v, %v", actions, err)
	}
	if err := st.CommitAdoption(ctx, previous, "tpl", 4, "digest", nil, []byte("stale")); err != ErrDeviceChanged {
		t.Fatalf("stale approval after replacement: %v", err)
	}
	for _, identity := range []struct{ public, id string }{{"pub-old", "id-old"}, {"pub-third", "id-third"}} {
		if ok, err := st.RegisterEnrollment(ctx, d, identity.public, identity.id, 0); err != nil || ok {
			t.Fatalf("replacement authorization was reused: ok=%v err=%v", ok, err)
		}
	}

	// Deleting the record removes the binding entirely; the next enrollment is
	// then a fresh pending claim.
	if err := st.DeleteDevice(ctx, mac); err != nil {
		t.Fatalf("delete device: %v", err)
	}
	if _, err := st.GetDevice(ctx, mac); !isNotFound(err) {
		t.Fatalf("device row still present after delete: %v", err)
	}
	if ok, err := st.RegisterEnrollment(ctx, d, "pub-new", "id-new", 0); err != nil || !ok {
		t.Fatalf("enroll after delete: ok=%v err=%v, want accepted", ok, err)
	}
	d2, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if d2.State != StatePending {
		t.Errorf("state = %q, want pending", d2.State)
	}
}

func TestReleaseReplacementRejectsApprovalForPreviousKey(t *testing.T) {
	for _, id := range []string{"", "same-id"} {
		t.Run("id="+id, func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(t.TempDir() + "/test.db")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			d := Device{MAC: "aa:bb:cc:dd:ee:13"}
			if ok, err := st.RegisterEnrollment(ctx, d, "old", id, 0); err != nil || !ok {
				t.Fatalf("enroll: %v, %v", ok, err)
			}
			if err := st.Release(ctx, d.MAC); err != nil {
				t.Fatal(err)
			}
			previous, err := st.GetDevice(ctx, d.MAC)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := st.RegisterEnrollment(ctx, d, "new", id, 0); err != nil || !ok {
				t.Fatalf("replace key: %v, %v", ok, err)
			}
			if err := st.CommitAdoption(ctx, previous, "tpl", 1, "digest", nil, []byte("stale")); err != ErrDeviceChanged {
				t.Fatalf("stale approval must check the key even when enrollment ID is reused: %v", err)
			}
		})
	}
}

// A row without an enrollment binding (a MAC known only from discovery, or a
// legacy shared-secret device) must accept an incoming enrollment as a fresh
// claim, not compare an empty key against the presented one.
func TestRegisterEnrollmentFreshBinding(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const mac = "aa:bb:cc:dd:ee:10"
	if err := st.UpsertDiscovered(ctx, Device{MAC: mac, Hostname: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RegisterEnrollment(ctx, Device{MAC: mac}, "pub", "id", 0); err != nil || !ok {
		t.Fatalf("enroll on unbound row: ok=%v err=%v, want accepted", ok, err)
	}
	d, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != StatePending || d.EnrollmentID != "id" {
		t.Errorf("state/enrollment = %q/%q, want pending/id", d.State, d.EnrollmentID)
	}
	if public, _, err := st.EnrollmentCredentials(ctx, mac); err != nil || public != "pub" {
		t.Fatalf("fresh enrollment key was not persisted: %q, %v", public, err)
	}
	if ok, err := st.RegisterEnrollment(ctx, Device{MAC: mac}, "other", "other-id", 0); err != nil || ok {
		t.Fatalf("another key replaced a pending claim: ok=%v err=%v", ok, err)
	}
}

func TestReleaseReplacementAuthorizationEndsOnReadoption(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "mark", true: "commit"}[commit], func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(t.TempDir() + "/test.db")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			d := Device{MAC: "aa:bb:cc:dd:ee:12"}
			if ok, err := st.RegisterEnrollment(ctx, d, "pub", "id", 4); err != nil || !ok {
				t.Fatalf("enroll: %v, %v", ok, err)
			}
			if err := st.Release(ctx, d.MAC); err != nil {
				t.Fatal(err)
			}
			// Repeating the same claim must not invalidate the existing key.
			if ok, err := st.RegisterEnrollment(ctx, d, "pub", "id", 4); err != nil || !ok {
				t.Fatalf("repeat enrollment: %v, %v", ok, err)
			}
			d, err = st.GetDevice(ctx, d.MAC)
			if err != nil {
				t.Fatal(err)
			}
			if commit {
				err = st.CommitAdoption(ctx, d, "tpl", 1, "digest", nil, []byte("rev1"))
			} else {
				err = st.MarkAdopted(ctx, d.MAC, "tpl", 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := st.RegisterEnrollment(ctx, d, "new", "new-id", 0); err != nil || ok {
				t.Fatalf("readoption left replacement authorized: %v, %v", ok, err)
			}
		})
	}
}

// The managed reset path (same key, generation+1) must keep working
// independently of the delete recovery.
func TestRegisterEnrollmentManagedResetUnaffected(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const mac = "aa:bb:cc:dd:ee:11"
	if ok, err := st.RegisterEnrollment(ctx, Device{MAC: mac}, "pub", "id", 0); err != nil || !ok {
		t.Fatalf("first enroll: ok=%v err=%v", ok, err)
	}
	if ok, err := st.RegisterEnrollment(ctx, Device{MAC: mac}, "pub", "id-reset", 1); err != nil || !ok {
		t.Fatalf("managed reset: ok=%v err=%v, want accepted", ok, err)
	}
	d, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != StatePending || d.EnrollmentID != "id-reset" {
		t.Errorf("after reset: state=%q id=%q", d.State, d.EnrollmentID)
	}
}

func isNotFound(err error) bool { return err == ErrNotFound }
