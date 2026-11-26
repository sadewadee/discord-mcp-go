package tools

import "testing"

func TestVoiceRegion(t *testing.T) {
	if got := voiceRegion(""); got != nil {
		t.Errorf("voiceRegion(\"\") = %v, want nil (automatic)", got)
	}
	if got := voiceRegion("rotterdam"); got != "rotterdam" {
		t.Errorf("voiceRegion(rotterdam) = %v", got)
	}
}
