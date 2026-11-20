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
	if ok, err := st.RegisterEnrollment(ctx, d, "pub-old", "id-old", 0); err != nil || !ok {
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

	// Releasing the device keeps the binding, so it must not clear the
	// deadlock either.
	if err := st.Release(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.RegisterEnrollment(ctx, d, "pub-new", "id-new", 0); ok {
		t.Fatal("release must not clear the enrollment binding")
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
