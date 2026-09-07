package registry

import "testing"

func TestParseBuildsAnOrderedRegistry(t *testing.T) {
	r, err := Parse("velocity=velocity:9300, device=device:9400 ,geo=geo:9500")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{"device", "geo", "velocity"}
	got := r.IDs()
	if len(got) != len(want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
}

func TestParseRejectsAMalformedEntry(t *testing.T) {
	if _, err := Parse("velocity:9300"); err == nil {
		t.Fatal("Parse accepted an entry with no separator")
	}
}

func TestParseRejectsAnEmptySpecification(t *testing.T) {
	if _, err := Parse("  "); err == nil {
		t.Fatal("Parse accepted an empty specification")
	}
}

func TestParseRejectsAnEntryWithNoEndpoint(t *testing.T) {
	if _, err := Parse("velocity="); err == nil {
		t.Fatal("Parse accepted an agent with no endpoint")
	}
}
