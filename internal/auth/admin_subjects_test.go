package auth

import "testing"

func TestParseAdminSubjects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []AdminSubject
	}{
		{name: "empty", raw: "", want: nil},
		{
			name: "single",
			raw:  "https://accounts.google.com|1234567890",
			want: []AdminSubject{{Issuer: "https://accounts.google.com", Subject: "1234567890"}},
		},
		{
			name: "multiple with spacing",
			raw:  "https://accounts.google.com|111, https://issuer.example|222",
			want: []AdminSubject{
				{Issuer: "https://accounts.google.com", Subject: "111"},
				{Issuer: "https://issuer.example", Subject: "222"},
			},
		},
		{
			name: "malformed entries are skipped",
			raw:  "no-pipe-here,https://issuer.example|222,|missing-issuer,missing-subject|",
			want: []AdminSubject{{Issuer: "https://issuer.example", Subject: "222"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAdminSubjects(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsAdminSubject(t *testing.T) {
	subjects := []AdminSubject{{Issuer: "iss-a", Subject: "sub-a"}}
	if !IsAdminSubject(subjects, "iss-a", "sub-a") {
		t.Error("expected match")
	}
	if IsAdminSubject(subjects, "iss-a", "sub-b") {
		t.Error("expected no match for different subject")
	}
	if IsAdminSubject(subjects, "iss-b", "sub-a") {
		t.Error("expected no match for different issuer")
	}
}
