package actions

import "testing"

func TestFixedPowerCommentsUseBoundedJobID(t *testing.T) {
	jobID := "dt_12345678901234567890_extra"
	short := "dt_12345678901234567"
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "classic", got: ClassicPowerComment(jobID), want: "DoneThen job " + short + " completed"},
		{name: "plugin", got: PluginPowerComment(jobID), want: "DoneThen plugin job " + short + " completed"},
		{name: "after stop", got: AfterStopPowerComment(jobID), want: "DoneThen job " + short + ": Codex stopped"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("comment = %q, want %q", test.got, test.want)
			}
			if !IsFixedPowerComment(jobID, test.got) {
				t.Fatalf("fixed comment was rejected: %q", test.got)
			}
		})
	}
	if IsFixedPowerComment(jobID, "please run something else") {
		t.Fatal("model-controlled comment was accepted")
	}
}
