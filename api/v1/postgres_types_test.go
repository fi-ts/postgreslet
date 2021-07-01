/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package v1

import (
	"regexp"
	"testing"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_setSharedBufferSize(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "default",
			input:    "64Mi",
			expected: "64MB",
		},
		{
			name:     "min",
			input:    "32Mi",
			expected: "32MB",
		},
		{
			name:     "belowMin",
			input:    "31Mi",
			expected: "",
		},
		{
			name:     "Gibibytes",
			input:    "1Gi",
			expected: "1024MB",
		},
		{
			name:     "Kibibytes",
			input:    "102400Ki",
			expected: "100MB",
		},
		{
			name:     "Bytes",
			input:    "67108864",
			expected: "64MB",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "empty2",
			input:    " ",
			expected: "",
		},
		{
			name:     "invalid",
			input:    "64MB",
			expected: "",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			parameters := map[string]string{}

			setSharedBufferSize(parameters, tt.input)

			result, ok := parameters[SharedBufferParameterKey]
			if ok {
				if tt.expected != result {
					t.Errorf("Adapter.ScopedGet() assertion failed: expected %q, got %q\n", tt.expected, result)
				}
			} else {
				if tt.expected != "" {
					t.Errorf("Conversion failed")
				}
			}
		})
	}
}

func TestPostgres_generateTeamID(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
	}{
		{
			name:      "beginning with letter",
			projectID: "abc",
		},
		{
			name:      "beginning with number",
			projectID: "1bc",
		},
		{
			name:      "contains number in the middle",
			projectID: "a1c",
		},
		{
			name:      "ends with number",
			projectID: "ab1",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "ab-cd.ef@12",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "12@ab-cd.ef",
		},
		{
			name:      "more than 16 chars long",
			projectID: "abcdefabcdefabcdefabcdefabcdefab",
		},
		{
			name:      "more than 16 numbers long",
			projectID: "12345678901234567890123456789012",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			var dnsRegExp *regexp.Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]*[a-z0-9])?$")
			p := &Postgres{
				Spec: PostgresSpec{
					ProjectID: tt.projectID,
				},
			}
			got := p.generateTeamID()
			if !dnsRegExp.MatchString(got) {
				t.Errorf("Postgres.generateTeamID() got %v, not a valid DNS name", got)
			}
		})
	}
}
func TestPostgres_ToPeripheralResourceName(t *testing.T) {
	tests := []struct {
		name         string
		projectID    string
		postgresName string
	}{
		{
			name:         "beginning with letter",
			projectID:    "abc",
			postgresName: "bca",
		},
		{
			name:         "beginning with number",
			projectID:    "1bc",
			postgresName: "1cb",
		},
		{
			name:         "contains number in the middle",
			projectID:    "a1c",
			postgresName: "c1a",
		},
		{
			name:         "ends with number",
			projectID:    "ab1",
			postgresName: "ba1",
		},
		{
			name:         "contains non-alphanumeric characters",
			projectID:    "ab-cd.ef@12",
			postgresName: "ba.dc-fe@21",
		},
		{
			name:         "contains non-alphanumeric characters",
			projectID:    "12@ab-cd.ef",
			postgresName: "21@ba-dc.fe",
		},
		{
			name:         "more than 16 chars long",
			projectID:    "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
			postgresName: "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
		},
		{
			name:         "more than 16 numbers long",
			projectID:    "123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
			postgresName: "123456789012345678901234567890121234567890123456789012345678901212345678901234567890123456789012",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			var dnsRegExp *regexp.Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]*[a-z0-9])?$")
			p := &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: tt.postgresName,
				},
				Spec: PostgresSpec{
					ProjectID: tt.projectID,
				},
			}
			got := p.ToPeripheralResourceName()
			if !dnsRegExp.MatchString(got) {
				t.Errorf("Postgres.ToPeripheralResourceName() got %v, not a valid DNS name", got)
			}
		})
	}
}
