package ui

import (
	"reflect"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

func TestDecisionOfAllowsOnlyWithAValidFolder(t *testing.T) {
	got := DecisionOf(true, []string{"/work/a", "relative", "/work/a/", "/work/b"}, time.Hour)
	if want := (consent.Decision{Answer: consent.AnswerAllow, Folders: []string{"/work/a", "/work/b"}, Lasts: time.Hour}); !reflect.DeepEqual(got, want) {
		t.Errorf("allow with folders = %+v, want %+v", got, want)
	}
	for name, c := range map[string]struct {
		allow   bool
		folders []string
	}{
		"deny":                  {false, []string{"/work"}},
		"allow with no folder":  {true, nil},
		"allow, only relatives": {true, []string{"work", "./x"}},
	} {
		if got := DecisionOf(c.allow, c.folders, time.Hour); got.Answer != consent.AnswerDeny || got.Folders != nil {
			t.Errorf("%s: decision = %+v", name, got)
		}
	}
}

func TestDecisionOfCarriesHowLongTheAccessLastsAndNeverIsNotTheDefault(t *testing.T) {
	if got := DecisionOf(true, []string{"/work"}, consent.NoEnd); got.Lasts != consent.NoEnd || got.LastsOrDefault() != consent.NoEnd {
		t.Errorf("never became %+v", got)
	}
}
