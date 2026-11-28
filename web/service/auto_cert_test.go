package service

import "testing"

func TestCertEmailForIP(t *testing.T) {
	tests := []struct {
		ip   string
		want string
		ok   bool
	}{
		{"1.1.1.1", "ssl@1111.email", true},
		{"38.39.26.85", "ssl@38392685.email", true},
		{" 8.8.8.8 ", "ssl@8888.email", true},
		{"", "", false},
		{"2001:db8::1", "", false},
		{"example.com", "", false},
	}
	for _, test := range tests {
		got, err := certEmailForIP(test.ip)
		if test.ok {
			if err != nil || got != test.want {
				t.Fatalf("certEmailForIP(%q) = %q, %v; want %q", test.ip, got, err, test.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("certEmailForIP(%q) accepted invalid input: %q", test.ip, got)
		}
	}
}
