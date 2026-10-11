package main

import "testing"

func TestSelectRestoreTemplate(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		fail            bool
	}{
		{"new build", "", "restore123", false},
		{"active private template", `{"template":"source123","team":"team1","public":false,"deleted":false}`, "source123", false},
		{"deleted snapshot template", `{"template":"source123","team":"team1","public":false,"deleted":true}`, "restore123", false},
		{"foreign deleted template", `{"template":"source123","team":"team2","public":false,"deleted":true}`, "", true},
		{"public deleted template", `{"template":"source123","team":"team1","public":true,"deleted":true}`, "", true},
		{"unknown deletion state", `{"template":"source123","team":"team1","public":false}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectRestoreTemplate(tc.raw, "team1", "restore123")
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v; want %q, error=%v", got, err, tc.want, tc.fail)
			}
		})
	}
}
