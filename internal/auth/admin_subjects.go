package auth

import "strings"

// AdminSubject identifies a specific OIDC identity (issuer + subject) that
// should be bootstrapped as an admin on first login.
type AdminSubject struct {
	Issuer  string
	Subject string
}

// ParseAdminSubjects parses DOGECADE_ADMIN_SUBJECTS: a comma-separated list
// of "issuer|subject" pairs. Malformed entries (missing the "|") are
// skipped rather than erroring the whole config, since this is a small
// operator-maintained allowlist, not user input.
func ParseAdminSubjects(raw string) []AdminSubject {
	var subjects []AdminSubject
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		issuer, subject, ok := strings.Cut(entry, "|")
		if !ok || issuer == "" || subject == "" {
			continue
		}
		subjects = append(subjects, AdminSubject{Issuer: issuer, Subject: subject})
	}
	return subjects
}

// IsAdminSubject reports whether issuer/subject appears in subjects.
func IsAdminSubject(subjects []AdminSubject, issuer, subject string) bool {
	for _, s := range subjects {
		if s.Issuer == issuer && s.Subject == subject {
			return true
		}
	}
	return false
}
