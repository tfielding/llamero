package mask

import "testing"

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		"+15550000001":   "…0001",
		"uuid-1234-abcd": "…abcd",
		"1234":           "1234",
		"":               "",
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
