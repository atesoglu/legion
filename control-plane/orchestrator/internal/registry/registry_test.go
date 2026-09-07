package registry

import "testing"

func agents() []Agent {
	return []Agent{
		{ID: "velocity", Endpoint: "velocity:9300", Kind: Deterministic},
		{ID: "device", Endpoint: "device:9400", Kind: Deterministic},
		{ID: "behavioral", Endpoint: "behavioral:9700", Kind: Behavioral},
	}
}

func TestNewOrdersByIdentifier(t *testing.T) {
	r, err := New(agents())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := r.IDs()
	want := []string{"behavioral", "device", "velocity"}
	if len(got) != len(want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
}

func TestNewRejectsAnAgentWithoutAnEndpoint(t *testing.T) {
	// Refusing to start is the point: misconfiguration replaces the compile
	// error a hardcoded agent set would have produced.
	_, err := New([]Agent{{ID: "velocity"}})
	if err == nil {
		t.Fatal("New accepted an agent with no endpoint")
	}
}

func TestNewRejectsDuplicateIdentifiers(t *testing.T) {
	_, err := New([]Agent{
		{ID: "velocity", Endpoint: "a:1"},
		{ID: "velocity", Endpoint: "b:2"},
	})
	if err == nil {
		t.Fatal("New accepted a duplicate agent identifier")
	}
}

func TestNewRejectsAnEmptySet(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New accepted an empty agent set")
	}
}

func TestOfKindSelectsTheBudgetWindow(t *testing.T) {
	r, err := New(agents())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := len(r.OfKind(Deterministic)); got != 2 {
		t.Fatalf("deterministic agents = %d, want 2", got)
	}
	if got := len(r.OfKind(Behavioral)); got != 1 {
		t.Fatalf("behavioural agents = %d, want 1", got)
	}
}

func TestAgentsIsACopy(t *testing.T) {
	r, err := New(agents())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r.Agents()[0].ID = "mutated"
	if r.IDs()[0] == "mutated" {
		t.Fatal("Agents() exposed the registry's own slice")
	}
}
