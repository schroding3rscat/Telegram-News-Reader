package user

import "testing"

func TestParseSourceLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input      string
		username   string
		inviteHash string
		private    bool
	}{
		{"@durov", "durov", "", false},
		{"https://t.me/example_channel", "example_channel", "", false},
		{"t.me/+AbCdEf123", "", "AbCdEf123", true},
		{"https://telegram.me/joinchat/AbCdEf123", "", "AbCdEf123", true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSourceLink(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Username != test.username || got.InviteHash != test.inviteHash || got.Private != test.private {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestParseSourceLinkRejectsForeignHost(t *testing.T) {
	t.Parallel()
	if _, err := ParseSourceLink("https://example.com/channel"); err == nil {
		t.Fatal("expected foreign host to be rejected")
	}
}
