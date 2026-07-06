package output

import "testing"

func TestColorEnabled(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		flag bool
		tty  bool
		env  map[string]string
		want bool
	}{
		{"tty default", false, true, nil, true},
		{"pipe default", false, false, nil, false},
		{"flag wins", true, true, map[string]string{"FORCE_COLOR": "1"}, false},
		{"NO_COLOR wins over tty", false, true, map[string]string{"NO_COLOR": "1"}, false},
		{"FORCE_COLOR wins over pipe", false, false, map[string]string{"FORCE_COLOR": "1"}, true},
		{"TERM dumb disables", false, true, map[string]string{"TERM": "dumb"}, false},
	}
	for _, c := range cases {
		if got := ColorEnabled(c.flag, c.tty, env(c.env)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
