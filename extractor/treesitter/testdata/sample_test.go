package sample

import "testing"

func TestGreet(t *testing.T) {
	got := Greet("Alice")
	want := "Hello, Alice!"
	if got != want {
		t.Errorf("Greet(%q) = %q, want %q", "Alice", got, want)
	}
}
