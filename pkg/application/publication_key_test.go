package application_test

import (
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestPublicationKeyEncoding(t *testing.T) {
	tests := []struct {
		name string
		key  sdd.PublicationKey
		want string
	}{
		{"capture", sdd.PublicationKey{Session: "s_A", Sequence: 41, Discriminator: "newEntry"}, "v1:257e4e53e36196e613eaaed958a5177dba3307ca7b6de590b1614be95e80381c"},
		{"another session", sdd.PublicationKey{Session: "s_B", Sequence: 41, Discriminator: "newEntry"}, "v1:bcef344d4e999e4de1e19a85b33ee30372c715338927cab47273f5727fa47be1"},
		{"another intent event", sdd.PublicationKey{Session: "s_A", Sequence: 42, Discriminator: "newEntry"}, "v1:875d002b5edc56e99539a60f4876c726dc6a7999463b58bb20ec464b23aa87bd"},
		{"another operation", sdd.PublicationKey{Session: "s_A", Sequence: 41, Discriminator: "summary-correction"}, "v1:bce205b2a252a544f0424c363481f26abc06b8632010578cc8fa0de802e18104"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.key.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := test.key.String(); got != test.want {
				t.Fatalf("publication identity must keep its v1 encoding: got %q, want %q", got, test.want)
			}
		})
	}
}
