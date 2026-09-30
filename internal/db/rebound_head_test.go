package db

import "testing"

// The unvalidated rebound head is the rewritten-remote recovery's durable
// statement that the branch's push binding names a head no run validated.
// Only the recovery's compare-and-swap writes it, a rebind replaces it, and
// a later publication of the exact head clears it - so the marker can never
// outlive the provenance problem it reports.
func TestUnvalidatedReboundHeadLifecycle(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	repo, err := d.InsertRepoWithID("repo-1", "/work/repo", "https://example.com/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "refs/heads/feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}

	const ref = "refs/heads/feature/sync"
	if _, ok, err := d.GetUnvalidatedReboundHead(repo.ID, ref); err != nil || ok {
		t.Fatalf("missing marker = (%q, %v), want not found", "", ok)
	}

	if err := d.RecordUnvalidatedReboundHead(repo.ID, ref, "1111111111111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	head, ok, err := d.GetUnvalidatedReboundHead(repo.ID, ref)
	if err != nil || !ok || head != "1111111111111111111111111111111111111111" {
		t.Fatalf("recorded marker = (%q, %v, %v)", head, ok, err)
	}

	// A rebind of the same ref replaces the row instead of failing the
	// unique constraint.
	if err := d.RecordUnvalidatedReboundHead(repo.ID, ref, "2222222222222222222222222222222222222222"); err != nil {
		t.Fatal(err)
	}
	head, ok, err = d.GetUnvalidatedReboundHead(repo.ID, ref)
	if err != nil || !ok || head != "2222222222222222222222222222222222222222" {
		t.Fatalf("rebound marker = (%q, %v, %v), want the replacement head", head, ok, err)
	}

	// Markers are per ref: another branch's binding is untouched.
	if err := d.RecordUnvalidatedReboundHead(repo.ID, "refs/heads/other", "3333333333333333333333333333333333333333"); err != nil {
		t.Fatal(err)
	}

	// A publication of a different head clears nothing.
	if err := d.ClearUnvalidatedReboundHeadOnPublication(run.ID, ref, "4444444444444444444444444444444444444444"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetUnvalidatedReboundHead(repo.ID, ref); !ok {
		t.Fatal("a publication of a different head must not clear the marker")
	}

	// Any run of the marker's repository publishing the exact head clears it,
	// so a stale marker can never name a head the pipeline has validated.
	if err := d.ClearUnvalidatedReboundHeadOnPublication(run.ID, ref, "2222222222222222222222222222222222222222"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.GetUnvalidatedReboundHead(repo.ID, ref); err != nil || ok {
		t.Fatalf("marker after publication = (found %v, err %v), want cleared", ok, err)
	}
	// The other ref's marker survives.
	if _, ok, err := d.GetUnvalidatedReboundHead(repo.ID, "refs/heads/other"); err != nil || !ok {
		t.Fatalf("other ref's marker = (found %v, err %v), want kept", ok, err)
	}
}

func TestUnvalidatedReboundHeadRequiresRepoRefAndHead(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	if err := d.RecordUnvalidatedReboundHead("", "refs/heads/feature", "head"); err == nil {
		t.Fatal("empty repo id must be refused")
	}
	if err := d.RecordUnvalidatedReboundHead("repo-1", "", "head"); err == nil {
		t.Fatal("empty ref must be refused")
	}
	if err := d.RecordUnvalidatedReboundHead("repo-1", "refs/heads/feature", ""); err == nil {
		t.Fatal("empty head must be refused")
	}
}
