package semver

import "testing"

func TestSatisfies(t *testing.T) {
	cases := []struct {
		eco  Ecosystem
		c, v string
		want bool
	}{
		// npm
		{EcoNPM, "^1.2.0", "1.9.3", true},
		{EcoNPM, "^1.2.0", "2.0.0", false},
		{EcoNPM, "^0.3.1", "0.3.9", true},
		{EcoNPM, "^0.3.1", "0.4.0", false},
		{EcoNPM, "~1.2.0", "1.2.9", true},
		{EcoNPM, "~1.2.0", "1.3.0", false},
		{EcoNPM, "workspace:*", "7.0.0", true},
		{EcoNPM, "workspace:^", "7.0.0", true},
		{EcoNPM, "workspace:^1.0.0", "2.0.0", false},
		{EcoNPM, ">=1.0.0 <2.0.0", "1.5.0", true},
		{EcoNPM, "1.x || 3.x", "3.1.0", true},
		{EcoNPM, "1.x || 3.x", "2.1.0", false},
		{EcoNPM, "1.2.3 - 1.4.0", "1.4.0", true},
		{EcoNPM, "1.2.3", "1.2.4", false},
		// cargo: bare = caret
		{EcoCargo, "1", "1.7.0", true},
		{EcoCargo, "1", "2.0.0", false},
		{EcoCargo, "0.3", "0.3.5", true},
		{EcoCargo, "0.3", "0.4.0", false},
		{EcoCargo, ">=1.2, <1.5", "1.4.9", true},
		{EcoCargo, "=2.0.1", "2.0.2", false},
		// python
		{EcoPython, ">=0.3,<1", "0.9.0", true},
		{EcoPython, ">=0.3,<1", "1.0.0", false},
		{EcoPython, "~=1.4", "1.9.0", true},
		{EcoPython, "~=1.4.5", "1.5.0", false},
		{EcoPython, "==1.2.*", "1.2.7", true},
		{EcoPython, "!=1.5.0", "1.5.0", false},
		// go: require is a minimum (MVS)
		{EcoGo, "v1.4.0", "1.6.0", true},
		{EcoGo, "v1.4.0", "1.3.0", false},
	}
	for _, c := range cases {
		got, err := Satisfies(c.eco, c.c, c.v)
		if err != nil {
			t.Errorf("%s %q vs %s: %v", c.eco, c.c, c.v, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s %q vs %s = %v, want %v", c.eco, c.c, c.v, got, c.want)
		}
	}
}
